package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/0xfakeSpike/ollupi/environment"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool/mcp"
	"github.com/coder/websocket"
)

func TestAnUnknownErrorStaysOnTheServer(t *testing.T) {
	internal := serverError(errors.New("no such table at /srv/shard/agent.db"))
	if internal.Code != jsonrpc.CodeServerError || strings.Contains(internal.Message, "/srv") {
		t.Errorf("an unknown error: %+v", internal)
	}
	known := serverError(fmt.Errorf("app: %w: /nowhere is unavailable", app.ErrInvalidPath))
	if known.Code != jsonrpc.CodeInvalidParams || !strings.Contains(known.Message, "/nowhere") {
		t.Errorf("a known error: %+v", known)
	}
}

type files struct{ kept sync.Map }

func (f *files) Put(_ context.Context, name string, data []byte) error {
	f.kept.Store(name, data)
	return nil
}

func (f *files) Get(_ context.Context, name string) ([]byte, error) {
	data, ok := f.kept.Load(name)
	if !ok {
		return nil, store.ErrNotFound
	}
	return data.([]byte), nil
}

func (f *files) Delete(_ context.Context, name string) error {
	f.kept.Delete(name)
	return nil
}

func (*files) DeletePrefix(context.Context, string) error { return nil }

type client struct {
	t    *testing.T
	base string
	ws   *websocket.Conn
	next int
	kept []message
}

type message struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *jsonrpc.Error   `json:"error,omitempty"`
}

func (c *client) send(v any) {
	c.t.Helper()
	raw, _ := json.Marshal(v)
	if err := c.ws.Write(context.Background(), websocket.MessageText, raw); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() message {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	var m message
	json.Unmarshal(raw, &m)
	return m
}

func (c *client) reply(method string, params any) message {
	c.t.Helper()
	c.next++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params})
	for {
		m := c.read()
		if m.ID == nil || m.Method != "" || string(*m.ID) != fmt.Sprint(c.next) {
			c.kept = append(c.kept, m)
			continue
		}
		return m
	}
}

func (c *client) call(method string, params any, result any) {
	c.t.Helper()
	m := c.reply(method, params)
	if m.Error != nil {
		c.t.Fatalf("%s: %+v", method, m.Error)
	}
	json.Unmarshal(m.Result, result)
}

func (c *client) until(method string) message {
	c.t.Helper()
	for len(c.kept) > 0 {
		m := c.kept[0]
		c.kept = c.kept[1:]
		if m.Method == method {
			return m
		}
	}
	for {
		if m := c.read(); m.Method == method {
			return m
		}
	}
}

// served is the one model the tests' catalog serves.
var served = []account.ModelProvider{{ID: "main", API: account.OpenAI, Models: []account.ProviderModel{{Name: "m", Limits: llm.Limits{Context: 200_000, Output: 8_000}}}}}

func connect(t *testing.T) (store.Store, *client) {
	t.Helper()
	st, dial := serve(t)
	return st, dial("secret")
}

// serve runs a gateway whose catalog serves main/m. dial connects with a
// token and initializes: "secret" acts as the user u, and "operator" as u
// and the deployment's operator.
func serve(t *testing.T) (store.Store, func(token string) *client) {
	t.Helper()
	st, dial, _ := serveWelcoming(t)
	return st, dial
}

// serveWelcoming is serve, and the users the gateway welcomed as it made
// them.
func serveWelcoming(t *testing.T) (store.Store, func(token string) *client, *[]string) {
	t.Helper()
	welcomed := new([]string)
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Update(ctx, func(tx store.Tx) error { return tx.SetCatalog(served) }); err != nil {
		t.Fatal(err)
	}
	envs, err := environment.New(st, map[string]sandbox.Provider{"local": sandbox.Local{Dir: t.TempDir(), Config: sandbox.Config{Mode: string(sandbox.ModeFullAccess)}}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { envs.Close() })
	users := mcp.NewUsers(ctx, mcp.Client{Name: "ollupi", Version: "test"}, st, envs.In)
	t.Cleanup(func() { users.Close() })
	a := app.New(st, catalog(served), users.Tools, users.Statuses, envs, &files{}, cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})
	if err := a.EnsureUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(a, st.Feed(), Options{Welcome: func(_ context.Context, user string) error {
		*welcomed = append(*welcomed, user)
		return nil
	}, Authenticate: func(_ context.Context, token string) (auth.Identity, error) {
		switch token {
		case "secret":
			return auth.Identity{User: "u"}, nil
		case "operator":
			return auth.Identity{User: "u", Operator: true}, nil
		}
		return auth.Identity{}, auth.ErrUnknownToken
	}})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	return st, func(token string) *client {
		t.Helper()
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(web.URL, "http"), &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		c := &client{t: t, base: web.URL, ws: ws}
		var init InitializeResult
		c.call(methodInitialize, InitializeParams{}, &init)
		if init.ProtocolSHA256 != protocolSHA256 {
			t.Errorf("initialize %+v", init)
		}
		return c
	}, welcomed
}

func TestAClientHearsWhatChangesAndAnswersWhatWaitsOnIt(t *testing.T) {
	ctx := context.Background()
	st, c := connect(t)
	var started ThreadResult
	c.call("thread/start", ThreadStartParams{Title: "chat"}, &started)
	var turn TurnResult
	c.call("turn/start", TurnStartParams{ThreadID: started.Thread.ID, Input: []UserInput{{Type: "text", Text: "build it"}}}, &turn)
	if turn.Turn.Status != TurnInProgress {
		t.Errorf("the turn %+v", turn.Turn)
	}
	var item ItemChangedNotification
	json.Unmarshal(c.until(notifyItemChanged).Params, &item)
	if item.Item.Type != conversation.ItemUserMessage || item.Item.Text != "build it" || item.Seq == 0 {
		t.Errorf("the message %+v", item)
	}

	var page ThreadPageResult
	c.call("thread/read", ThreadPageParams{ThreadID: started.Thread.ID}, &page)
	if page.Seq < item.Seq || len(page.Items) != 1 || page.Turns[0].Status != TurnInProgress {
		t.Errorf("the page at %d: %+v", page.Seq, page)
	}

	err := st.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append(started.Thread.ID,
			conversation.Entry{Turn: turn.Turn.ID, Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: turn.Turn.ID, Item: "c1", Body: conversation.ToolCall{Call: "x", Tool: "run_command", Arguments: `{"command":"make"}`, Response: "r"}},
			conversation.Entry{Turn: turn.Turn.ID, Item: "c1", Body: conversation.ApprovalAsked{Approval: "a1", Content: "make"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	asked := c.until(requestApproval)
	var params ApprovalRequestParams
	json.Unmarshal(asked.Params, &params)
	if params.ApprovalID != "a1" || params.Content != "make" || len(params.AvailableDecisions) == 0 {
		t.Fatalf("the request %+v", params)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "id": asked.ID, "result": ApprovalResponse{Decision: conversation.Accept}})
	var status ThreadChangedNotification
	for {
		m := c.read()
		if m.Method == notifyThreadChanged {
			json.Unmarshal(m.Params, &status)
		}
		var changed ItemChangedNotification
		accepted := m.Method == notifyItemChanged && json.Unmarshal(m.Params, &changed) == nil &&
			changed.Item.ID == "c1" && changed.Item.Approval.Decision == conversation.Accept
		if accepted {
			break
		}
	}
	if status.Thread == nil || status.Thread.Status != conversation.ThreadActive || status.Seq <= page.Seq {
		t.Errorf("after the decision: %+v", status)
	}
}

func TestAClientSeesWhichHubConversationSentAMessage(t *testing.T) {
	ctx := context.Background()
	st, c := connect(t)
	var plan, notes ThreadResult
	c.call("thread/start", ThreadStartParams{Title: "plan"}, &plan)
	c.call("thread/start", ThreadStartParams{Title: "notes"}, &notes)
	err := st.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append(notes.Thread.ID,
			conversation.Entry{Turn: "t", Body: conversation.TurnQueued{Model: llm.Model{Name: "m", Connection: "main/m"}}},
			conversation.Entry{Turn: "t", Item: "m", Body: conversation.UserMessage{Text: "write notes",
				From: &conversation.Sender{Thread: plan.Thread.ID, Title: "plan"}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var item ItemChangedNotification
	json.Unmarshal(c.until(notifyItemChanged).Params, &item)
	if item.Item.Text != "write notes" || item.Item.SentByThreadID != plan.Thread.ID {
		t.Errorf("the message %+v", item.Item)
	}
	var page ThreadPageResult
	c.call("thread/read", ThreadPageParams{ThreadID: notes.Thread.ID}, &page)
	if len(page.Items) != 1 || page.Items[0].SentByThreadID != plan.Thread.ID {
		t.Errorf("the page %+v", page.Items)
	}
}

func TestAUserChoosesAmongTheDeploymentsModels(t *testing.T) {
	_, c := connect(t)
	var models ModelListResult
	c.call("model/list", Empty{}, &models)
	if len(models.Data) != 1 || models.Data[0].Connection != "main/m" || models.Default != "main/m" {
		t.Fatalf("models %+v", models)
	}
	if m := c.reply("settings/update", map[string]any{"model": "nobody/x"}); m.Error == nil || m.Error.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("a model the deployment does not serve was chosen: %+v", m.Error)
	}
	var row SettingsRow
	c.call("settings/update", map[string]any{"model": "main/m", "learnModel": "main/m"}, &row)
	if row.Model != "main/m" || row.LearnModel != "main/m" {
		t.Fatalf("settings %+v", row)
	}
	raw, _ := json.Marshal(models)
	if strings.Contains(string(raw), "apiKey") || strings.Contains(string(raw), "baseUrl") {
		t.Fatalf("the catalog's account reached a user: %s", raw)
	}
}

// catalog is a deployment's fixed models.
type catalog []account.ModelProvider

func (c catalog) Providers() []account.ModelProvider { return c }

func TestAListAndTheChangesAfterItShareOneSequence(t *testing.T) {
	_, c := connect(t)

	var todos TodoListResult
	c.call("todo/list", TodoListParams{}, &todos)
	var added TodoResult
	c.call("todo/add", TodoAddParams{Title: "write docs"}, &added)
	var todo TodoChangedNotification
	json.Unmarshal(c.until(notifyTodoChanged).Params, &todo)
	if todo.Todo.ID != added.Todo.ID || todo.Seq <= todos.Seq {
		t.Errorf("todo/changed at %d after todo/list at %d: %+v", todo.Seq, todos.Seq, todo)
	}
	c.call("todo/list", TodoListParams{}, &todos)
	if todos.Seq < todo.Seq || len(todos.Data) != 1 {
		t.Errorf("todo/list at %d after todo/changed at %d: %+v", todos.Seq, todo.Seq, todos)
	}

	var settings SettingsReadResult
	c.call("settings/read", Empty{}, &settings)
	learn := !settings.Learn
	var updated SettingsRow
	c.call("settings/update", SettingsUpdateParams{Learn: &learn}, &updated)
	var changed SettingsChangedNotification
	json.Unmarshal(c.until(notifySettingsChanged).Params, &changed)
	if changed.Learn != learn || changed.Seq <= settings.Seq {
		t.Errorf("settings/changed at %d after settings/read at %d: %+v", changed.Seq, settings.Seq, changed)
	}

	var workspaces WorkspaceListResult
	c.call("workspace/list", Empty{}, &workspaces)
	var workspace WorkspaceResult
	c.call("workspace/add", WorkspaceAddParams{Path: t.TempDir()}, &workspace)
	var moved WorkspaceChangedNotification
	json.Unmarshal(c.until(notifyWorkspaceChanged).Params, &moved)
	if moved.Workspace.ID != workspace.Workspace.ID || moved.Seq <= workspaces.Seq {
		t.Errorf("workspace/changed at %d after workspace/list at %d: %+v", moved.Seq, workspaces.Seq, moved)
	}

	var threads ThreadListResult
	c.call("thread/list", ThreadListParams{}, &threads)
	var started ThreadResult
	c.call("thread/start", ThreadStartParams{Title: "chat"}, &started)
	var thread ThreadChangedNotification
	json.Unmarshal(c.until(notifyThreadChanged).Params, &thread)
	if thread.ThreadID != started.Thread.ID || thread.Seq <= threads.Seq {
		t.Errorf("thread/changed at %d after thread/list at %d: %+v", thread.Seq, threads.Seq, thread)
	}
	c.call("thread/list", ThreadListParams{}, &threads)
	if threads.Seq < thread.Seq {
		t.Errorf("thread/list at %d after thread/changed at %d", threads.Seq, thread.Seq)
	}
}

func TestReadsSentTogetherAnswerEachAndAskOnce(t *testing.T) {
	ctx := context.Background()
	st, c := connect(t)
	var started ThreadResult
	c.call("thread/start", ThreadStartParams{Title: "chat"}, &started)
	var turn TurnResult
	c.call("turn/start", TurnStartParams{ThreadID: started.Thread.ID, Input: []UserInput{{Type: "text", Text: "build it"}}}, &turn)
	err := st.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append(started.Thread.ID,
			conversation.Entry{Turn: turn.Turn.ID, Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: turn.Turn.ID, Item: "c1", Body: conversation.ToolCall{Call: "x", Tool: "run_command", Arguments: `{"command":"make"}`, Response: "r"}},
			conversation.Entry{Turn: turn.Turn.ID, Item: "c1", Body: conversation.ApprovalAsked{Approval: "a1", Content: "make"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c.until(requestApproval)

	const sent = 6
	for i := range sent {
		c.send(map[string]any{"jsonrpc": "2.0", "id": 100 + i, "method": "thread/resume", "params": ThreadPageParams{ThreadID: started.Thread.ID}})
	}
	c.send(map[string]any{"jsonrpc": "2.0", "id": 200, "method": "thread/rename", "params": ThreadRenameParams{ThreadID: started.Thread.ID, Title: "renamed"}})
	answered, asked := map[string]bool{}, 0
	for len(answered) < sent+1 {
		m := c.read()
		switch {
		case m.Method == requestApproval:
			asked++
		case m.ID != nil && m.Method == "":
			if m.Error != nil {
				t.Fatalf("reply %s: %+v", *m.ID, m.Error)
			}
			answered[string(*m.ID)] = true
		}
	}
	if asked != 0 {
		t.Errorf("an approval still waiting on this connection was asked again %d times", asked)
	}
}

func TestAnAttachmentIsReadOverHTTP(t *testing.T) {
	_, c := connect(t)
	post := func(token, name string, body []byte) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, c.base+UploadPath, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	if res := post("", "notes.txt", []byte("x")); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an upload without a token: %d", res.StatusCode)
	}
	if res := post("secret", "blob.bin", []byte{0, 1}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("an upload neither an image nor text: %d", res.StatusCode)
	}
	res := post("secret", "笔记.txt", []byte("ship on friday\n"))
	var up UploadResult
	if err := json.NewDecoder(res.Body).Decode(&up); err != nil || res.StatusCode != http.StatusOK || up.Name != "笔记.txt" || up.Size != 15 {
		t.Fatalf("the upload: %d %+v %v", res.StatusCode, up, err)
	}
	var started ThreadResult
	c.call("thread/start", ThreadStartParams{Title: "chat"}, &started)
	var turn TurnResult
	c.call("turn/start", TurnStartParams{ThreadID: started.Thread.ID, Input: []UserInput{{Type: "text", Text: "read this"}, {Type: "upload", UploadID: up.UploadID}}}, &turn)
	var item ItemChangedNotification
	json.Unmarshal(c.until(notifyItemChanged).Params, &item)
	if len(item.Item.Attachments) != 1 {
		t.Fatalf("the message %+v", item.Item)
	}
	att := item.Item.Attachments[0]

	get := func(token, thread, id, origin string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, c.base+"/attachments/"+thread+"/"+id, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	res = get("secret", started.Thread.ID, att.ID, "http://localhost:5173")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "ship on friday\n" || res.Header.Get("Content-Type") != att.MimeType ||
		res.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("the attachment: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res := get("", started.Thread.ID, att.ID, ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: %d", res.StatusCode)
	}
	if res := get("secret", started.Thread.ID, "nope", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("an attachment the thread does not have: %d", res.StatusCode)
	}
	if res := get("secret", started.Thread.ID, att.ID, "https://elsewhere.example"); res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("an origin not allowed was let in: %v", res.Header)
	}

	preflight, _ := http.NewRequest(http.MethodOptions, c.base+"/attachments/"+started.Thread.ID+"/"+att.ID, nil)
	preflight.Header.Set("Origin", "http://localhost:5173")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization")
	answer, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	answer.Body.Close()
	if answer.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(strings.ToLower(answer.Header.Get("Access-Control-Allow-Headers")), "authorization") {
		t.Errorf("the preflight: %d %v", answer.StatusCode, answer.Header)
	}
}

func TestAUserKeepsTheirOwnSkills(t *testing.T) {
	_, c := connect(t)
	set := SkillSetParams{Name: "deploy", Description: "How to deploy.", Instructions: "Run the script.",
		Roles: []agent.Name{agent.FullMain, agent.BackendSub}, Files: []SkillFile{{Path: "scripts/run.sh", Text: "echo run", Executable: true}}}
	var put SkillResult
	c.call("skill/set", set, &put)
	var changed SkillChangedNotification
	json.Unmarshal(c.until(notifySkillChanged).Params, &changed)
	if changed.Skill == nil || changed.Skill.Digest != put.Skill.Digest || len(changed.Skill.Files) != 1 {
		t.Errorf("skill/changed after skill/set: %+v", changed)
	}

	var list SkillListResult
	c.call("skill/list", Empty{}, &list)
	if len(list.Data) != 1 || len(list.Data[0].Roles) != 2 || list.Data[0].Files[0] != (SkillFileInfo{Path: "scripts/run.sh", ByteSize: 8}) {
		t.Errorf("skill/list: %+v", list)
	}
	var read SkillReadResult
	c.call("skill/read", SkillNameParams{Name: "deploy"}, &read)
	if read.Instructions != "Run the script." || len(read.Files) != 1 || read.Files[0] != set.Files[0] {
		t.Errorf("skill/read: %+v", read)
	}

	bad := set
	bad.Roles = []agent.Name{"main-agent"}
	if m := c.reply("skill/set", bad); m.Error == nil || m.Error.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("a skill of no role: %+v", m.Error)
	}

	c.call("skill/delete", SkillNameParams{Name: "deploy"}, &Empty{})
	json.Unmarshal(c.until(notifySkillChanged).Params, &changed)
	if changed.Name != "deploy" || changed.Skill != nil {
		t.Errorf("skill/changed after skill/delete: %+v", changed)
	}
	if m := c.reply("skill/read", SkillNameParams{Name: "deploy"}); m.Error == nil || m.Error.Code != errNotFound {
		t.Errorf("a deleted skill: %+v", m.Error)
	}
}

func TestOnlyTheOperatorReadsAndChangesTheCatalogAndEveryoneHearsTheModels(t *testing.T) {
	_, dial := serve(t)
	user, op := dial("secret"), dial("operator")

	for _, m := range []string{"catalog/read", "catalog/set"} {
		reply := user.reply(m, CatalogSetParams{Data: []ModelProviderRow{}})
		if reply.Error == nil || reply.Error.Code != errForbidden {
			t.Fatalf("a user's %s: %+v", m, reply)
		}
	}

	var read CatalogResult
	op.call("catalog/read", Empty{}, &read)
	if len(read.Data) != 1 || read.Data[0].ID != "main" || read.Data[0].API != account.OpenAI || read.Seq == 0 {
		t.Fatalf("the catalog %+v", read)
	}

	// Refused whole: the catalog stays as it was.
	bad := op.reply("catalog/set", CatalogSetParams{Data: []ModelProviderRow{{ID: "a/b", API: account.OpenAI, Models: []account.ProviderModel{{Name: "x", Limits: llm.Limits{Context: 1000, Output: 100}}}}}})
	if bad.Error == nil || bad.Error.Code != jsonrpc.CodeInvalidParams || !strings.Contains(bad.Error.Message, "'/'") {
		t.Fatalf("a catalog that does not check: %+v", bad)
	}

	next := []ModelProviderRow{
		{ID: "deep", API: account.OpenAI, BaseURL: "https://api.deepseek.com", APIKey: "sk-d", Models: []account.ProviderModel{{Name: "flash", Limits: llm.Limits{Context: 128_000, Output: 8_000}}}},
		{ID: "main", API: account.OpenAI, Models: []account.ProviderModel{{Name: "m", Limits: llm.Limits{Context: 200_000, Output: 8_000}}}},
	}
	op.call("catalog/set", CatalogSetParams{Data: next}, &Empty{})

	var changed CatalogChangedNotification
	json.Unmarshal(op.until(notifyCatalogChanged).Params, &changed)
	if len(changed.Data) != 2 || changed.Data[0].APIKey != "sk-d" || changed.Seq <= read.Seq {
		t.Fatalf("the operator heard %+v", changed)
	}
	for name, c := range map[string]*client{"user": user, "operator": op} {
		var models ModelChangedNotification
		json.Unmarshal(c.until(notifyModelChanged).Params, &models)
		if len(models.Data) != 2 || models.Default != "deep/flash" || models.Seq != changed.Seq {
			t.Fatalf("the %s heard the models %+v", name, models)
		}
	}
	// A user hears the models, never the catalog and its keys.
	for _, m := range user.kept {
		if m.Method == notifyCatalogChanged {
			t.Fatalf("a user heard the catalog: %s", m.Params)
		}
	}

	var list ModelListResult
	user.call("model/list", Empty{}, &list)
	if len(list.Data) != 2 || list.Default != "deep/flash" || list.Seq < changed.Seq {
		t.Fatalf("model/list %+v", list)
	}
}

func TestTheOperatorMakesAndNamesUsersAndAUserNamesOnlyThemself(t *testing.T) {
	_, dial, welcomed := serveWelcoming(t)
	user, op := dial("secret"), dial("operator")

	var me UserResult
	user.call("user/read", Empty{}, &me)
	if me.User != (UserRow{ID: "u"}) {
		t.Fatalf("u reads %+v", me)
	}
	for _, m := range []string{"user/list", "user/create"} {
		if reply := user.reply(m, UserCreateParams{Name: "x"}); reply.Error == nil || reply.Error.Code != errForbidden {
			t.Fatalf("a user's %s: %+v", m, reply)
		}
	}

	var made UserResult
	op.call("user/create", UserCreateParams{Name: "  Work  "}, &made)
	if made.User.ID == "" || made.User.Name != "Work" || len(*welcomed) != 1 || (*welcomed)[0] != made.User.ID {
		t.Fatalf("made %+v, welcomed %v", made, *welcomed)
	}
	if bad := op.reply("user/create", UserCreateParams{Name: " "}); bad.Error == nil || bad.Error.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("a user without a name: %+v", bad)
	}
	var changed UserChangedNotification
	json.Unmarshal(op.until(notifyUserChanged).Params, &changed)
	if changed.User != made.User || changed.Seq == 0 {
		t.Fatalf("the operator heard %+v", changed)
	}

	// A user renames themself, not another; the operator renames anyone.
	if reply := user.reply("user/update", UserUpdateParams{ID: made.User.ID, Name: "Mine"}); reply.Error == nil || reply.Error.Code != errForbidden {
		t.Fatalf("u renamed another: %+v", reply)
	}
	user.call("user/update", UserUpdateParams{ID: "u", Name: "Me"}, &me)
	var heard UserChangedNotification
	json.Unmarshal(user.until(notifyUserChanged).Params, &heard)
	if heard.User != (UserRow{ID: "u", Name: "Me"}) {
		t.Fatalf("u heard %+v", heard)
	}
	// u heard of themself, never of the user the operator made.
	for _, m := range user.kept {
		if m.Method == notifyUserChanged && strings.Contains(string(m.Params), made.User.ID) {
			t.Fatalf("u heard of another user: %s", m.Params)
		}
	}
	op.call("user/update", UserUpdateParams{ID: made.User.ID, Name: "Office"}, &made)
	if reply := op.reply("user/update", UserUpdateParams{ID: "nobody", Name: "x"}); reply.Error == nil || reply.Error.Code != errNotFound {
		t.Fatalf("renamed a user who is not there: %+v", reply)
	}

	var list UserListResult
	op.call("user/list", Empty{}, &list)
	if len(list.Data) != 2 || list.Seq == 0 {
		t.Fatalf("users %+v", list)
	}
	names := map[string]string{}
	for _, u := range list.Data {
		names[u.ID] = u.Name
	}
	if names["u"] != "Me" || names[made.User.ID] != "Office" {
		t.Fatalf("users %+v", list)
	}
}

func TestADeploymentWithoutModelsListsNoneNotNull(t *testing.T) {
	st, dial := serve(t)
	if err := st.Update(context.Background(), func(tx store.Tx) error { return tx.SetCatalog(nil) }); err != nil {
		t.Fatal(err)
	}
	c := dial("secret")
	reply := c.reply("model/list", Empty{})
	if reply.Error != nil || !strings.Contains(string(reply.Result), `"data":[]`) {
		t.Fatalf("model/list %s %+v", reply.Result, reply.Error)
	}
}

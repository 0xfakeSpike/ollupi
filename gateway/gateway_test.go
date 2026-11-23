package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

type models struct{}

func (models) Models() []llm.Model { return []llm.Model{{Name: "m", Connection: "main"}} }
func (models) Limits(llm.Model) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 8_000}, nil
}

type noFiles struct{}

func (noFiles) Put(context.Context, string, []byte) error   { return nil }
func (noFiles) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (noFiles) Delete(context.Context, string) error        { return nil }
func (noFiles) DeletePrefix(context.Context, string) error  { return nil }

type client struct {
	t    *testing.T
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

func connect(t *testing.T) (store.Store, *client) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	boxes := func(string) (sandbox.Box, error) { return sandbox.Here() }
	users := mcp.NewUsers(ctx, mcp.Client{Name: "ollupi", Version: "test"}, st, boxes)
	t.Cleanup(func() { users.Close() })
	a := app.New(st, models{}, users.Tools, users.Statuses, boxes, noFiles{}, cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})
	if err := a.EnsureUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(a, st.Feed(), Options{Authenticate: func(_ context.Context, token string) (string, error) {
		if token != "secret" {
			return "", auth.ErrUnknownToken
		}
		return "u", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(web.URL, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	c := &client{t: t, ws: ws}
	var init InitializeResult
	c.call(methodInitialize, InitializeParams{}, &init)
	if init.ProtocolSHA256 != protocolSHA256 {
		t.Errorf("initialize %+v", init)
	}
	return st, c
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
			conversation.Entry{Turn: "t", Body: conversation.TurnQueued{Model: llm.Model{Name: "m", Connection: "main"}}},
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

func TestAProfileIsMadeListedAndRenamed(t *testing.T) {
	_, c := connect(t)
	var made UserResult
	c.call("user/create", UserCreateParams{Name: "Work", Color: "teal"}, &made)
	if made.User.ID == "" || made.User.Name != "Work" || made.User.Color != "teal" {
		t.Fatalf("made %+v", made.User)
	}
	var list UserListResult
	c.call("user/list", Empty{}, &list)
	if len(list.Data) != 2 || list.Current != "u" {
		t.Fatalf("list %+v", list)
	}
	var renamed UserResult
	c.call("user/rename", UserRenameParams{UserID: made.User.ID, Name: "Studio"}, &renamed)
	if renamed.User.Name != "Studio" {
		t.Fatalf("renamed %+v", renamed.User)
	}
	var coloured UserResult
	c.call("user/color", UserColorParams{UserID: made.User.ID, Color: "rose"}, &coloured)
	if coloured.User.Color != "rose" {
		t.Fatalf("coloured %+v", coloured.User)
	}
}

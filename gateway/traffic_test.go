package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	"github.com/0xfakeSpike/ollupi/tools/shell"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTrafficMatchesContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	fake := &fakeModel{}
	model := httptest.NewServer(fake)
	defer model.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var server *Server
	jobs := cron.New(db, time.UTC, func(id, threadID string) { server.CronChanged(id, threadID) })
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		s := sdk.NewServer(&sdk.Implementation{Name: "remote", Version: "1"}, nil)
		sdk.AddTool(s, &sdk.Tool{Name: "lookup", Description: "Looks a thing up."}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{}, nil, nil
		})
		return s
	}, nil))
	defer remote.Close()
	servers := mcp.Connect(ctx, mcp.Client{}, []mcp.Server{{Name: "remote", URL: remote.URL}, {Name: "broken", Command: "/nonexistent/mcp-server"}})
	defer servers.Close()
	mem, err := memory.New(db, nil, memory.Options{Use: true})
	if err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{
		Name:   "test",
		Models: []llm.Model{{Name: "gpt-4o", Connection: "fake"}},
		Settings: permission.Settings{ApprovalPolicy: string(permission.OnRequest),
			Rules: permission.Rules{Deny: []string{"broken__drop_tables"}}},
		Connections: map[string]*llm.OpenAI{"fake": llm.NewOpenAI(model.URL, "", 4, nil)},
		Providers:   append([]tools.Provider{touchTool{}, jobs}, servers.Providers()...),
		Unavailable: servers.Unavailable(),
		Memory:      mem,
		DB:          db,
		OnEvent:     func(e agent.Event) { server.Deliver(e) },
	})
	if server, err = NewServer(ag, Options{Token: "secret", Memory: mem, Jobs: jobs, MCP: servers}); err != nil {
		t.Fatal(err)
	}
	agentCtx, stop := context.WithCancel(ctx)
	if err := ag.Start(agentCtx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()
	gw := httptest.NewServer(server)
	defer gw.Close()

	c := dial(ctx, t, gw.URL)
	defer c.ws.Close(websocket.StatusNormalClosure, "")

	var init InitializeResult
	c.call("initialize", InitializeParams{ClientInfo: ClientInfo{Name: "test", Version: "1"}}, &init)
	sum := sha256.Sum256(c.contractFile)
	if init.ProtocolSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("initialize reports protocol %s, but protocol.schema.json hashes to %x", init.ProtocolSHA256, sum)
	}
	c.notify("initialized", Empty{})
	c.call("model/list", Empty{}, nil)
	var toolList ToolListResult
	c.call("tool/list", Empty{}, &toolList)
	checkToolSchemas(t, toolList.Data)
	var mcpServers MCPServerListResult
	c.call("mcpServer/list", Empty{}, &mcpServers)
	if len(mcpServers.Data) != 2 || !mcpServers.Data[0].Connected || mcpServers.Data[0].Tools != 1 || mcpServers.Data[1].Connected {
		t.Errorf("mcpServer/list: %+v", mcpServers.Data)
	}
	c.call("workspace/list", Empty{}, nil)
	c.call("thread/list", repo.ThreadFilter{}, nil)

	var ws WorkspaceResult
	c.call("workspace/add", WorkspaceAddParams{Path: t.TempDir(), Name: "project"}, &ws)
	c.await("workspace/changed", nil)
	workspaceID := ws.Workspace.ID
	c.call("workspace/rename", WorkspaceRenameParams{WorkspaceID: workspaceID, Name: "renamed"}, nil)
	c.call("workspace/updatePermissions", WorkspacePermissionsParams{WorkspaceID: workspaceID,
		PermissionSettings: repo.PermissionSettings{ApprovalPolicy: string(permission.OnRequest)}}, nil)

	saved := time.Now().UTC()
	for _, m := range []repo.MemoryRow{
		{ID: "g1", Scope: repo.MemoryGlobal, Topic: "style", Kind: "preference", Text: "Answers briefly."},
		{ID: "w1", Scope: repo.MemoryWorkspace, WorkspaceID: workspaceID, Topic: "build", Kind: "fact", Text: "Builds with go."},
		{ID: "n1", Scope: repo.MemoryGeneral, Topic: "trip", Kind: "fact", Text: "Plans a trip in November."},
	} {
		m.Status, m.CreatedAt, m.UpdatedAt = repo.MemoryActive, saved, saved
		if err := db.SaveMemory(m); err != nil {
			t.Fatal(err)
		}
	}

	var th ThreadResult
	c.call("thread/start", ThreadStartParams{WorkspaceID: workspaceID}, &th)
	c.await("thread/started", nil)
	threadID := th.Thread.ID
	c.call("thread/updatePermissions", ThreadPermissionsParams{ThreadID: threadID}, nil)

	turn := c.startTurn(threadID, "hello")
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	turn = c.startTurn(threadID, "please touch the file")
	approval := c.await(requestApproval, nil)
	queued := c.startTurn(threadID, "and then say hi")
	c.await("turn/queued", nil)
	c.call("turn/steer", TurnSteerParams{ThreadID: threadID, ExpectedTurnID: turn, Input: []UserInput{{Type: "text", Text: "thanks"}}}, nil)
	c.call("thread/resume", ThreadPageParams{ThreadID: threadID}, nil)
	c.respond(approval, ApprovalResponse{Decision: repo.DecisionAccept})
	c.awaitTurnEnd(turn, repo.TurnCompleted)
	c.awaitTurnEnd(queued, repo.TurnCompleted)

	turn = c.startTurn(threadID, "ask me something")
	question := c.await(requestUserInput, nil)
	c.respond(question, UserInputResponse{Answers: userinput.Answers{}})
	c.await("error", nil)
	question = c.await(requestUserInput, nil)
	c.respond(question, UserInputResponse{Answers: userinput.Answers{"color": {Answers: []string{"blue"}}}})
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	turn = c.startTurn(threadID, "delegate this")
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	turn = c.startTurn(threadID, "recall how to answer")
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	c.awaitTurnEnd(c.startTurn(threadID, "make a plan"), repo.TurnCompleted)
	for _, words := range []string{"cron-create", "cron-pause", "cron-list", "cron-delete"} {
		c.toolTurn(threadID, words)
	}

	var daily repo.CronJobRow
	c.call("cron/create", CronCreateParams{ThreadID: threadID, Name: "daily", Spec: "@daily", Prompt: "daily check"}, &daily)
	c.cronChanged(daily.ID, func(j *repo.CronJobView) bool { return j != nil && j.Name == "daily" && j.ThreadPreview == "hello" })
	c.call("cron/list", CronListParams{ThreadID: threadID}, nil)
	paused := true
	c.call("cron/pause", CronPauseParams{ThreadID: threadID, Name: "daily", Paused: &paused}, nil)
	c.cronChanged(daily.ID, func(j *repo.CronJobView) bool { return j != nil && j.Paused })
	c.call("cron/delete", CronDeleteParams{ThreadID: threadID, Name: "daily"}, nil)
	c.cronChanged(daily.ID, func(j *repo.CronJobView) bool { return j == nil })
	now := time.Now().UTC()
	if err := db.CreateCronJob(repo.CronJobRow{ID: "due", ThreadID: threadID, Name: "due", Spec: "@hourly", Prompt: "scheduled check",
		NextRunAt: now.Add(-time.Minute), CreatedAt: now}, 10); err != nil {
		t.Fatal(err)
	}
	go jobs.Run(agentCtx, func(threadID, text, jobID string) error {
		_, err := ag.StartTurn(threadID, agent.Input{Text: text, CronJobID: jobID})
		return err
	})
	ran := c.await("turn/completed", func(params json.RawMessage) bool {
		var n TurnNotification
		return json.Unmarshal(params, &n) == nil && n.Turn.CronJobID == "due"
	})
	var ranTurn TurnNotification
	json.Unmarshal(ran.Params, &ranTurn)
	c.cronChanged("due", func(j *repo.CronJobView) bool {
		return j != nil && j.LastTurnID == ranTurn.Turn.ID && j.LastRunAt != nil && j.NextRunAt.After(now)
	})

	turn = c.startTurn(threadID, "ask again")
	c.await(requestUserInput, nil)
	c.call("turn/interrupt", TurnInterruptParams{ThreadID: threadID, TurnID: turn}, nil)
	c.awaitTurnEnd(turn, repo.TurnInterrupted)

	var page ThreadPageResult
	c.call("thread/read", ThreadPageParams{ThreadID: threadID}, &page)
	if prompts, searched := checkMemory(t, page.Items, "w1"); prompts < 5 || !searched {
		t.Errorf("%d prompts reported memory, searched %v; want every prompt and a search", prompts, searched)
	}

	var general ThreadResult
	c.call("thread/start", ThreadStartParams{}, &general)
	c.await("thread/started", nil)
	c.awaitTurnEnd(c.startTurn(general.Thread.ID, "hello"), repo.TurnCompleted)
	c.call("thread/read", ThreadPageParams{ThreadID: general.Thread.ID}, &page)
	if prompts, _ := checkMemory(t, page.Items, "n1"); prompts != 1 {
		t.Errorf("%d prompts reported memory in the general thread, want 1", prompts)
	}

	var settings repo.SettingsRow
	c.call("settings/read", Empty{}, &settings)
	if !settings.Redaction {
		t.Error("redaction is off by default")
	}
	c.awaitTurnEnd(c.startTurn(general.Thread.ID, "mail alice@example.com"), repo.TurnCompleted)
	c.refused("settings/update", SettingsUpdateParams{}, jsonrpc.CodeInvalidParams, "redaction")
	off := false
	c.call("settings/update", SettingsUpdateParams{Redaction: &off}, nil)
	c.await("settings/changed", func(params json.RawMessage) bool {
		var n repo.SettingsRow
		return json.Unmarshal(params, &n) == nil && !n.Redaction
	})
	c.awaitTurnEnd(c.startTurn(general.Thread.ID, "mail bob@example.com"), repo.TurnCompleted)
	fake.mu.Lock()
	sent := strings.Join(fake.prompts, "\n")
	fake.mu.Unlock()
	if strings.Contains(sent, "alice@example.com") || !strings.Contains(sent, "mail <EMAIL:") || !strings.Contains(sent, "mail bob@example.com") {
		t.Errorf("the model was sent:\n%s", sent)
	}
	c.call("thread/read", ThreadPageParams{ThreadID: threadID, Limit: 2, BeforeSeq: 4}, nil)
	c.call("thread/list", repo.ThreadFilter{WorkspaceID: &workspaceID}, nil)

	for _, e := range []repo.MemoryRow{
		{ID: "m1", Scope: repo.MemoryGeneral, Topic: "colors", Kind: "preference", Text: "Likes blue."},
		{ID: "m2", Scope: repo.MemoryWorkspace, WorkspaceID: workspaceID, Topic: "shell", Kind: "preference", Text: "Uses zsh."},
		{ID: "m3", Scope: repo.MemoryWorkspace, WorkspaceID: workspaceID, Topic: "editor", Kind: "preference", Text: "Uses vim."},
		{ID: "m4", Scope: repo.MemoryGlobal, Topic: "editor", Kind: "preference", Text: "Uses emacs."},
	} {
		e.Status, e.CreatedAt, e.UpdatedAt = repo.MemoryActive, now, now
		if err := db.SaveMemory(e); err != nil {
			t.Fatal(err)
		}
	}
	var listed MemoryListResult
	c.call("memory/list", MemoryListParams{Scope: repo.MemoryGeneral}, &listed)
	if len(listed.Data) != 2 {
		t.Errorf("general memories %+v, want n1 and m1", listed.Data)
	}
	c.refused("memory/list", MemoryListParams{Scope: repo.MemoryWorkspace}, jsonrpc.CodeInvalidParams, "workspaceId")
	var moved MemoryResult
	for _, want := range []repo.MemoryRow{
		{Scope: repo.MemoryGlobal},
		{Scope: repo.MemoryGeneral},
	} {
		c.call("memory/move", MemoryMoveParams{ID: "m1"}, &moved)
		if moved.Memory.Scope != want.Scope || moved.Memory.WorkspaceID != "" || moved.Memory.OriginWorkspaceID != "" {
			t.Errorf("m1 moved to %s %q from %q, want %s", moved.Memory.Scope, moved.Memory.WorkspaceID, moved.Memory.OriginWorkspaceID, want.Scope)
		}
	}
	c.refused("memory/move", MemoryMoveParams{ID: "m3"}, jsonrpc.CodeInvalidParams, "already there")
	c.call("memory/move", MemoryMoveParams{ID: "m2"}, &moved)
	if moved.Memory.Scope != repo.MemoryGlobal || moved.Memory.OriginWorkspaceID != workspaceID {
		t.Errorf("m2 moved to %s from %q", moved.Memory.Scope, moved.Memory.OriginWorkspaceID)
	}
	moved = MemoryResult{}
	c.call("memory/move", MemoryMoveParams{ID: "m2"}, &moved)
	if moved.Memory.Scope != repo.MemoryWorkspace || moved.Memory.WorkspaceID != workspaceID || moved.Memory.OriginWorkspaceID != "" {
		t.Errorf("m2 moved back to %s %q", moved.Memory.Scope, moved.Memory.WorkspaceID)
	}
	c.call("memory/status", Empty{}, nil)
	c.call("memory/update", MemoryUpdateParams{ID: "m1", Text: "Likes green."}, nil)
	c.call("memory/forget", MemoryForgetParams{ID: "m1"}, nil)

	c.refused("workspace/updatePermissions", WorkspacePermissionsParams{WorkspaceID: workspaceID,
		PermissionSettings: repo.PermissionSettings{Rules: repo.PermissionRules{Allow: []string{"broken__creat_issue"}}}},
		jsonrpc.CodeInvalidParams, `the MCP server "broken" is not connected`)
	if _, err := db.UpdateWorkspace(workspaceID, func(w repo.WorkspaceRow) (repo.WorkspaceRow, error) {
		w.Settings.Rules.Allow = []string{"gitlab__merge"}
		return w, nil
	}); err != nil {
		t.Fatal(err)
	}
	turn = c.startTurn(threadID, "hello again")
	failed := c.await("turn/completed", func(params json.RawMessage) bool {
		var n TurnNotification
		return json.Unmarshal(params, &n) == nil && n.Turn.ID == turn
	})
	var ended TurnNotification
	json.Unmarshal(failed.Params, &ended)
	if ended.Turn.Status != repo.TurnFailed || !strings.Contains(ended.Turn.Error, `workspace "renamed"`) || !strings.Contains(ended.Turn.Error, `no tool provider is named "gitlab"`) {
		t.Errorf("turn with a rule for a removed server ended %s: %s", ended.Turn.Status, ended.Turn.Error)
	}

	c.call("cron/create", CronCreateParams{ThreadID: threadID, Name: "weekly", Spec: "@weekly", Prompt: "weekly check"}, nil)
	c.call("cron/pause", CronPauseParams{ThreadID: threadID, Name: "weekly", Paused: &paused}, nil)
	c.call("cron/create", CronCreateParams{ThreadID: general.Thread.ID, Name: "nightly", Spec: "@midnight", Prompt: "nightly check"}, nil)
	archived := true
	c.call("workspace/archive", WorkspaceArchiveParams{WorkspaceID: workspaceID, Archived: &archived}, nil)
	var all CronListResult
	c.call("cron/list", CronListParams{}, &all)
	byName := map[string]repo.CronJobView{}
	for _, j := range all.Data {
		byName[j.Name] = j
	}
	if j := byName["weekly"]; len(all.Data) != 3 || !j.Paused || j.WorkspaceID != workspaceID || j.ParentThreadID != "" || j.LastRunAt != nil {
		t.Errorf("all tasks %+v", all.Data)
	}
	if j := byName["nightly"]; j.WorkspaceID != "" || j.ThreadID != general.Thread.ID || j.ThreadPreview != "hello" {
		t.Errorf("task outside any workspace %+v", j)
	}
	if j := byName["due"]; j.LastTurnID != ranTurn.Turn.ID {
		t.Errorf("task that ran %+v", j)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range protocol.methods {
		if !c.seen["method "+name] {
			t.Errorf("the session never called %s", name)
		}
	}
	for name := range protocol.notifications {
		if !c.seen["notification "+name] {
			t.Errorf("the session never received %s", name)
		}
	}
	for name := range protocol.serverRequests {
		if !c.seen["request "+name] {
			t.Errorf("the session never received %s", name)
		}
	}
	for name := range protocol.clientNotifications {
		if !c.seen["client notification "+name] {
			t.Errorf("the session never sent %s", name)
		}
	}
	for name, tool := range builtinTools {
		if name == shell.ToolName {
			continue
		}
		if tool.arguments != nil && !c.seen["arguments "+name] {
			t.Errorf("no call of %s checked its arguments", name)
		}
		if tool.output != nil && !c.seen["output "+name] {
			t.Errorf("no call of %s checked its output", name)
		}
	}
}

func checkMemory(t *testing.T, items []repo.ItemRow, local string) (prompts int, searched bool) {
	t.Helper()
	snapshots := 0
	for _, it := range items {
		switch {
		case it.Type == repo.ItemMemoryContext:
			snapshots++
			if len(it.Memory) != 2 || it.Memory[0].ID != "g1" || it.Memory[0].Scope != repo.MemoryGlobal || it.Memory[1].ID != local {
				t.Errorf("snapshot %+v, want g1 then %s", it.Memory, local)
			}
		case it.Tool == memory.ToolSearch:
			searched = it.Status == repo.ItemCompleted && it.Result != nil && strings.Contains(string(it.Result.Output), `"g1"`)
		}
		if s := it.ContextStats; s != nil && s.GlobalMemory != nil {
			prompts++
			if s.MemoryTokens == 0 || !slices.Equal(s.GlobalMemory.IDs, []string{"g1"}) || !slices.Equal(s.LocalMemory.IDs, []string{local}) {
				t.Errorf("context stats %+v, global %+v, local %+v", s, s.GlobalMemory, s.LocalMemory)
			}
		}
	}
	if snapshots != 1 {
		t.Errorf("%d snapshots, want 1", snapshots)
	}
	return prompts, searched
}

type touchTool struct{}

func (touchTool) Name() (string, string) { return "files", "Touches files." }

func (touchTool) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name:        "touch",
		Description: "Touches a file.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Annotations: &tools.Annotations{Title: "Touch"},
	}}, nil
}

func (touchTool) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{Output: map[string]any{"touched": true}}, nil
}

type fakeModel struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls++
	id := fmt.Sprintf("call_%d", f.calls)
	f.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	var text string
	json.Unmarshal(last.Content, &text)
	if last.Role == "user" {
		f.mu.Lock()
		f.prompts = append(f.prompts, text)
		f.mu.Unlock()
	}

	var chunks []map[string]any
	call := func(name, arguments string) {
		chunks = append(chunks, map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
		}}})
	}
	switch {
	case last.Role == "user" && strings.Contains(text, "touch"):
		call("touch", `{}`)
	case last.Role == "user" && strings.Contains(text, "ask"):
		call(userinput.Tool, `{"questions":[{"id":"color","header":"Color","question":"Which color?"}]}`)
	case last.Role == "user" && strings.Contains(text, "delegate"):
		call(subagent.Tool, `{"description":"Check a thing","prompt":"Check the thing and report."}`)
	case last.Role == "user" && strings.Contains(text, "recall"):
		call(memory.ToolSearch, `{"query":"answers"}`)
	case last.Role == "user" && strings.Contains(text, "make a plan"):
		call(plan.Tool, `{"explanation":"Two steps.","plan":[{"step":"Look around","status":"in_progress"},{"step":"Report","status":"pending"}]}`)
	case last.Role == "user" && strings.Contains(text, "cron-create"):
		call(cron.ToolCreateJob, `{"name":"standup","spec":"0 9 * * 1-5","prompt":"Write the standup notes."}`)
	case last.Role == "user" && strings.Contains(text, "cron-pause"):
		call(cron.ToolPauseJob, `{"name":"standup","paused":true}`)
	case last.Role == "user" && strings.Contains(text, "cron-list"):
		call(cron.ToolListJobs, `{}`)
	case last.Role == "user" && strings.Contains(text, "cron-delete"):
		call(cron.ToolDeleteJob, `{"name":"standup"}`)
	default:
		chunks = append(chunks, map[string]any{"reasoning_content": "Thinking it over."}, map[string]any{"content": "All "}, map[string]any{"content": "done."})
	}
	finish := "stop"
	if len(chunks) == 1 {
		finish = "tool_calls"
	}

	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	choice := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "chatcmpl", "object": "chat.completion.chunk", "created": 1, "model": "gpt-4o",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	for _, delta := range chunks {
		send(choice(delta, nil))
	}
	send(choice(map[string]any{}, finish))
	send(map[string]any{"id": "chatcmpl", "object": "chat.completion.chunk", "created": 1, "model": "gpt-4o", "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

type client struct {
	t            *testing.T
	ctx          context.Context
	ws           *websocket.Conn
	contractFile []byte
	contract     contract

	mu         sync.Mutex
	nextID     int
	calls      map[string]string
	responses  map[string]chan jsonrpc.Message
	inbox      []jsonrpc.Message
	arrived    chan struct{}
	validators map[string]*jsonschema.Resolved
	seen       map[string]bool
}

func dial(ctx context.Context, t *testing.T, url string) *client {
	raw, err := os.ReadFile("protocol.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, ctx: ctx, contractFile: raw, calls: map[string]string{}, responses: map[string]chan jsonrpc.Message{},
		arrived: make(chan struct{}, 1), validators: map[string]*jsonschema.Resolved{}, seen: map[string]bool{}}
	if err := json.Unmarshal(raw, &c.contract); err != nil {
		t.Fatal(err)
	}
	c.ws, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.ws.SetReadLimit(maxMessageBytes * 8)
	go c.read()
	return c
}

func (c *client) read() {
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var m jsonrpc.Message
		if err := json.Unmarshal(data, &m); err != nil {
			c.t.Errorf("the gateway sent invalid JSON-RPC: %v", err)
			continue
		}
		c.mu.Lock()
		switch {
		case m.Method != "" && m.ID != nil:
			c.seen["request "+m.Method] = true
			if r, ok := c.contract.ServerRequests[m.Method]; ok {
				c.validate("server request "+m.Method, r.Params, m.Params)
			} else {
				c.t.Errorf("the gateway sent request %s, which the contract does not declare", m.Method)
			}
			c.inbox = append(c.inbox, m)
		case m.Method != "":
			c.seen["notification "+m.Method] = true
			if n, ok := c.contract.Notifications[m.Method]; ok {
				c.validate("notification "+m.Method, n, m.Params)
			} else {
				c.t.Errorf("the gateway sent notification %s, which the contract does not declare", m.Method)
			}
			if m.Method == notifyItemCompleted {
				c.validateToolCall(m.Params)
			}
			c.inbox = append(c.inbox, m)
		default:
			method := c.calls[string(m.ID)]
			if m.Error == nil {
				c.validate("result of "+method, c.contract.Methods[method].Result, m.Result)
			}
			if ch := c.responses[string(m.ID)]; ch != nil {
				ch <- m
			}
		}
		c.mu.Unlock()
		select {
		case c.arrived <- struct{}{}:
		default:
		}
	}
}

func (c *client) validate(what string, s *jsonschema.Schema, raw json.RawMessage) {
	if s == nil {
		c.t.Errorf("%s: the contract has no schema for it", what)
		return
	}
	rs := c.validators[s.Ref]
	if rs == nil {
		var fresh contract
		if err := json.Unmarshal(c.contractFile, &fresh); err != nil {
			c.t.Errorf("%s: %v", what, err)
			return
		}
		var err error
		if rs, err = (&jsonschema.Schema{Defs: fresh.Defs, Ref: s.Ref}).Resolve(nil); err != nil {
			c.t.Errorf("%s: resolve %s: %v", what, s.Ref, err)
			return
		}
		c.validators[s.Ref] = rs
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		c.t.Errorf("%s: %v", what, err)
		return
	}
	if err := rs.Validate(instance); err != nil {
		c.t.Errorf("%s does not match %s: %v\n%s", what, s.Ref, err, raw)
	}
}

func (c *client) send(m jsonrpc.Message) {
	data, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.Write(c.ctx, websocket.MessageText, data); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) call(method string, params, result any) {
	c.t.Helper()
	reply := c.exchange(method, params)
	if reply.Error != nil {
		c.t.Fatalf("%s: %v", method, reply.Error)
	}
	if result != nil {
		if err := json.Unmarshal(reply.Result, result); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *client) refused(method string, params any, code int, why string) {
	c.t.Helper()
	reply := c.exchange(method, params)
	if reply.Error == nil || reply.Error.Code != code || !strings.Contains(reply.Error.Message, why) {
		c.t.Errorf("%s: got %+v, want error %d about %q", method, reply.Error, code, why)
	}
}

func (c *client) exchange(method string, params any) jsonrpc.Message {
	c.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.seen["method "+method] = true
	m, ok := c.contract.Methods[method]
	if !ok {
		c.t.Fatalf("the contract does not declare method %s", method)
	}
	c.validate("params of "+method, m.Params, raw)
	c.nextID++
	id := fmt.Sprint(c.nextID)
	c.calls[id] = method
	ch := make(chan jsonrpc.Message, 1)
	c.responses[id] = ch
	c.mu.Unlock()
	c.send(jsonrpc.Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw})
	select {
	case reply := <-ch:
		return reply
	case <-c.ctx.Done():
		c.t.Fatalf("%s: no reply", method)
		return jsonrpc.Message{}
	}
}

func (c *client) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.seen["client notification "+method] = true
	c.validate("client notification "+method, c.contract.ClientNotifications[method], raw)
	c.mu.Unlock()
	c.send(jsonrpc.Message{JSONRPC: "2.0", Method: method, Params: raw})
}

func (c *client) respond(request jsonrpc.Message, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.validate("response to "+request.Method, c.contract.ServerRequests[request.Method].Result, raw)
	c.mu.Unlock()
	c.send(jsonrpc.Message{JSONRPC: "2.0", ID: request.ID, Result: raw})
}

func (c *client) await(method string, match func(params json.RawMessage) bool) jsonrpc.Message {
	c.t.Helper()
	return c.awaitAny(method, func(m jsonrpc.Message) bool { return m.Method == method && (match == nil || match(m.Params)) })
}

func (c *client) awaitAny(what string, match func(jsonrpc.Message) bool) jsonrpc.Message {
	c.t.Helper()
	for {
		c.mu.Lock()
		for i, m := range c.inbox {
			if match(m) {
				c.inbox = append(c.inbox[:i], c.inbox[i+1:]...)
				c.mu.Unlock()
				return m
			}
		}
		c.mu.Unlock()
		select {
		case <-c.arrived:
		case <-c.ctx.Done():
			c.t.Fatalf("no %s arrived", what)
		}
	}
}

func (c *client) validateToolCall(params json.RawMessage) {
	var n ItemNotification
	if err := json.Unmarshal(params, &n); err != nil || !n.Item.IsCall() {
		return
	}
	it := n.Item
	tool, ok := c.contract.BuiltinTools[it.Tool]
	if !ok {
		return
	}
	if tool.Arguments != nil {
		c.validate("arguments of "+it.Tool, tool.Arguments, json.RawMessage(it.Arguments))
		c.seen["arguments "+it.Tool] = true
	}
	if tool.Output != nil && it.Status == repo.ItemCompleted && it.Error == "" && it.Result != nil {
		c.validate("output of "+it.Tool, tool.Output, it.Result.Output)
		c.seen["output "+it.Tool] = true
	}
}

func (c *client) toolTurn(threadID, text string) {
	c.t.Helper()
	turn := c.startTurn(threadID, text)
	for {
		m := c.awaitAny("approval or end of "+text, func(m jsonrpc.Message) bool {
			if m.Method == requestApproval {
				return true
			}
			var n TurnNotification
			return m.Method == "turn/completed" && json.Unmarshal(m.Params, &n) == nil && n.Turn.ID == turn
		})
		if m.Method == requestApproval {
			c.respond(m, ApprovalResponse{Decision: repo.DecisionAccept})
			continue
		}
		var n TurnNotification
		json.Unmarshal(m.Params, &n)
		if n.Turn.Status != repo.TurnCompleted {
			c.t.Fatalf("%q: the turn ended %s: %s", text, n.Turn.Status, n.Turn.Error)
		}
		return
	}
}

func checkToolSchemas(t *testing.T, offered []tools.ToolDescriptor) {
	t.Helper()
	for name, tool := range builtinTools {
		if tool.arguments == nil {
			continue
		}
		want, err := tools.InputSchemaOf(tool.arguments)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i < 0 {
			t.Errorf("built-in tool %s is not offered", name)
			continue
		}
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(offered[i].InputSchema)
		var wantJSON, gotJSON any
		json.Unmarshal(a, &wantJSON)
		json.Unmarshal(b, &gotJSON)
		if !reflect.DeepEqual(wantJSON, gotJSON) {
			t.Errorf("%s offers the input schema\n%s\nbut its arguments type %s gives\n%s", name, b, tool.arguments, a)
		}
	}
}

func (c *client) cronChanged(id string, ok func(*repo.CronJobView) bool) {
	c.t.Helper()
	c.await(notifyCronChanged, func(params json.RawMessage) bool {
		var n CronChangedNotification
		return json.Unmarshal(params, &n) == nil && n.ID == id && n.ThreadID != "" && ok(n.Job)
	})
}

func (c *client) startTurn(threadID, text string) string {
	c.t.Helper()
	var r TurnResult
	c.call("turn/start", TurnStartParams{ThreadID: threadID, Input: []UserInput{{Type: "text", Text: text}}}, &r)
	return r.Turn.ID
}

func (c *client) awaitTurnEnd(turnID string, status repo.TurnStatus) {
	c.t.Helper()
	m := c.await("turn/completed", func(params json.RawMessage) bool {
		var n TurnNotification
		return json.Unmarshal(params, &n) == nil && n.Turn.ID == turnID
	})
	var n TurnNotification
	json.Unmarshal(m.Params, &n)
	if n.Turn.Status != status {
		c.t.Fatalf("turn %s ended %s (%s), want %s", turnID, n.Turn.Status, n.Turn.Error, status)
	}
}

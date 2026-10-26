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
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
	"github.com/google/jsonschema-go/jsonschema"
)

// TestTrafficMatchesContract runs a gateway session against a fake model and
// validates every message the gateway sends and receives against
// protocol.schema.json, and that the session used every message the
// protocol declares.
func TestTrafficMatchesContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	model := httptest.NewServer(&fakeModel{})
	defer model.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := cron.New(db, time.UTC)
	mem, err := memory.New(db, nil, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var server *Server
	ag := agent.New(agent.Config{
		Name:        "test",
		Models:      []llm.Model{{Name: "gpt-4o", Connection: "fake"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.OnRequest)},
		Connections: map[string]*llm.OpenAI{"fake": llm.NewOpenAI(model.URL, "", 4, nil)},
		Providers:   []tools.Provider{touchTool{}, jobs},
		Memory:      mem,
		DB:          db,
		OnEvent:     func(e agent.Event) { server.Deliver(e) },
	})
	if server, err = NewServer(ag, Options{Token: "secret", Memory: mem, Jobs: jobs}); err != nil {
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
	c.call("tool/list", Empty{}, nil)
	c.call("workspace/list", Empty{}, nil)
	c.call("thread/list", repo.ThreadFilter{}, nil)

	var ws WorkspaceResult
	c.call("workspace/add", WorkspaceAddParams{Path: t.TempDir(), Name: "project"}, &ws)
	c.await("workspace/changed", nil)
	workspaceID := ws.Workspace.ID
	c.call("workspace/rename", WorkspaceRenameParams{WorkspaceID: workspaceID, Name: "renamed"}, nil)
	c.call("workspace/updatePermissions", WorkspacePermissionsParams{WorkspaceID: workspaceID,
		PermissionSettings: repo.PermissionSettings{ApprovalPolicy: string(permission.OnRequest)}}, nil)

	var th ThreadResult
	c.call("thread/start", ThreadStartParams{WorkspaceID: workspaceID}, &th)
	c.await("thread/started", nil)
	threadID := th.Thread.ID
	c.call("thread/updatePermissions", ThreadPermissionsParams{ThreadID: threadID}, nil)

	// A streamed reply.
	turn := c.startTurn(threadID, "hello")
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	// A call that needs approval, with a message queued and one steered in while it waits.
	turn = c.startTurn(threadID, "please touch the file")
	approval := c.await(requestApproval, nil)
	queued := c.startTurn(threadID, "and then say hi")
	c.await("turn/queued", nil)
	c.call("turn/steer", TurnSteerParams{ThreadID: threadID, ExpectedTurnID: turn, Input: []UserInput{{Type: "text", Text: "thanks"}}}, nil)
	c.call("thread/resume", ThreadPageParams{ThreadID: threadID}, nil)
	c.respond(approval, ApprovalResponse{Decision: repo.DecisionAccept})
	c.awaitTurnEnd(turn, repo.TurnCompleted)
	c.awaitTurnEnd(queued, repo.TurnCompleted)

	// A question, answered wrongly first.
	turn = c.startTurn(threadID, "ask me something")
	question := c.await(requestUserInput, nil)
	c.respond(question, UserInputResponse{Answers: userinput.Answers{}})
	c.await("error", nil)
	question = c.await(requestUserInput, nil)
	c.respond(question, UserInputResponse{Answers: userinput.Answers{"color": {Answers: []string{"blue"}}}})
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	// A subagent.
	turn = c.startTurn(threadID, "delegate this")
	c.awaitTurnEnd(turn, repo.TurnCompleted)

	// Scheduled tasks, managed and run.
	c.call("cron/create", CronCreateParams{ThreadID: threadID, Name: "daily", Spec: "@daily", Prompt: "daily check"}, nil)
	c.call("cron/list", CronListParams{ThreadID: threadID}, nil)
	paused := true
	c.call("cron/pause", CronPauseParams{ThreadID: threadID, Name: "daily", Paused: &paused}, nil)
	c.call("cron/delete", CronDeleteParams{ThreadID: threadID, Name: "daily"}, nil)
	now := time.Now().UTC()
	if err := db.CreateCronJob(repo.CronJobRow{ID: "due", ThreadID: threadID, Name: "due", Spec: "@hourly", Prompt: "scheduled check",
		NextRunAt: now.Add(-time.Minute), CreatedAt: now}, 10); err != nil {
		t.Fatal(err)
	}
	go jobs.Run(agentCtx, func(threadID, text, jobID string) error {
		_, err := ag.StartTurn(threadID, agent.Input{Text: text, CronJobID: jobID})
		return err
	})
	c.await("turn/completed", func(params json.RawMessage) bool {
		var n TurnNotification
		return json.Unmarshal(params, &n) == nil && n.Turn.CronJobID == "due"
	})

	// An interrupted turn.
	turn = c.startTurn(threadID, "ask again")
	c.await(requestUserInput, nil)
	c.call("turn/interrupt", TurnInterruptParams{ThreadID: threadID, TurnID: turn}, nil)
	c.awaitTurnEnd(turn, repo.TurnInterrupted)

	c.call("thread/read", ThreadPageParams{ThreadID: threadID}, nil)
	c.call("thread/read", ThreadPageParams{ThreadID: threadID, Limit: 2, BeforeSeq: 4}, nil)
	c.call("thread/list", repo.ThreadFilter{WorkspaceID: &workspaceID}, nil)

	if err := db.SaveMemory(repo.MemoryRow{ID: "m1", Topic: "colors", Kind: "preference", Text: "Likes blue.", Status: repo.MemoryActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c.call("memory/list", MemoryListParams{IncludeGlobal: true}, nil)
	c.call("memory/status", Empty{}, nil)
	c.call("memory/update", MemoryUpdateParams{ID: "m1", Text: "Likes green."}, nil)
	c.call("memory/forget", MemoryForgetParams{ID: "m1"}, nil)

	archived := true
	c.call("workspace/archive", WorkspaceArchiveParams{WorkspaceID: workspaceID, Archived: &archived}, nil)

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
}

// touchTool is a tool that is not read-only, so on-request asks before running it.
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

// fakeModel streams OpenAI chat completions. It answers a tool result with
// text, and a user message with the tool call its words ask for, or text.
type fakeModel struct {
	mu    sync.Mutex
	calls int
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

// client is a gateway client that checks every message against the contract.
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

// validate checks raw against the schema s refers to; the caller holds c.mu.
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
		if reply.Error != nil {
			c.t.Fatalf("%s: %v", method, reply.Error)
		}
		if result != nil {
			if err := json.Unmarshal(reply.Result, result); err != nil {
				c.t.Fatal(err)
			}
		}
	case <-c.ctx.Done():
		c.t.Fatalf("%s: no reply", method)
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

// await takes the first message named method that match accepts (nil accepts
// any), waiting for it if it has not arrived; it leaves other messages.
func (c *client) await(method string, match func(params json.RawMessage) bool) jsonrpc.Message {
	c.t.Helper()
	for {
		c.mu.Lock()
		for i, m := range c.inbox {
			if m.Method == method && (match == nil || match(m.Params)) {
				c.inbox = append(c.inbox[:i], c.inbox[i+1:]...)
				c.mu.Unlock()
				return m
			}
		}
		c.mu.Unlock()
		select {
		case <-c.arrived:
		case <-c.ctx.Done():
			c.t.Fatalf("no %s arrived", method)
		}
	}
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

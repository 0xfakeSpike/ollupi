package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
)

const (
	maxMessageBytes = 1 << 20
	defaultLimit    = 20
	maxLimit        = 100
	defaultItems    = 100
	maxItems        = 500
)

type Server struct {
	agent   *agent.Agent
	memory  *memory.Manager
	token   string
	hosts   []string
	methods map[string]Method
	origins []string
	mu      sync.Mutex
	deliver sync.Mutex
	client  *conn
	served  sync.WaitGroup
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.served.Add(1)
	defer s.served.Done()
	if status, why := s.authorize(r); status != 0 {
		http.Error(w, why, status)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		log.Printf("gateway: accept: %v", err)
		return
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{s: s, ws: ws, rpc: jsonrpc.NewConn(ws), asked: make(map[string]bool)}
	s.mu.Lock()
	previous := s.client
	s.client = c
	s.mu.Unlock()
	if previous != nil {
		go previous.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	defer func() {
		s.mu.Lock()
		if s.client == c {
			s.client = nil
		}
		s.mu.Unlock()
	}()

	stop := context.AfterFunc(r.Context(), func() { ws.Close(websocket.StatusGoingAway, "server shutting down") })
	defer stop()
	err = c.rpc.Serve(context.Background(), c)
	switch status := websocket.CloseStatus(err); {
	case status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || r.Context().Err() != nil:
		ws.Close(websocket.StatusNormalClosure, "")
	default:
		log.Printf("gateway: connection closed: %v", err)
		ws.Close(websocket.StatusInternalError, "connection error")
	}
}

func (s *Server) Deliver(e agent.Event) {
	s.deliver.Lock()
	defer s.deliver.Unlock()
	if c := s.current(); c != nil {
		c.event(e)
	}
}

func (s *Server) notify(method string, payload any) {
	if c := s.current(); c != nil {
		c.rpc.Notify(method, payload)
	}
}

func (s *Server) current() *conn {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil || !c.isInitialized() {
		return nil
	}
	return c
}

type conn struct {
	s   *Server
	ws  *websocket.Conn
	rpc *jsonrpc.Conn

	mu          sync.Mutex
	initialized bool
	asked       map[string]bool
}

func (c *conn) isInitialized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

func (c *conn) Notify(method string, _ json.RawMessage) {
	if method != methodInitialized {
		log.Printf("gateway: ignored client notification %q", method)
	}
}

func (c *conn) Call(method string, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	c.mu.Lock()
	initialized := c.initialized
	c.mu.Unlock()
	if method == methodInitialize {
		if initialized {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "already initialized")
		}
		var p initializeParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		after := func() {
			c.mu.Lock()
			c.initialized = true
			c.mu.Unlock()
			for _, r := range c.s.agent.AllPendingRequests() {
				c.request(r.Thread.ID, r.Item)
			}
		}
		return initializeResult{ServerInfo: serverInfo{Name: "ollupi", Version: version()}}, after, nil
	}
	if !initialized {
		return nil, nil, jsonrpc.Errorf(errNotInitialized, "call initialize first")
	}
	ag := c.s.agent
	switch method {
	case methodMemoryList, methodMemoryStatus, methodMemoryUpdate, methodMemoryForget:
		return c.memoryRequest(method, params)
	case methodWorkspaceList, methodWorkspaceAdd, methodWorkspaceRename, methodWorkspaceArchive, methodWorkspacePermissions:
		return c.workspaceRequest(method, params)
	case methodModelList:
		if err := jsonrpc.DecodeParams(params, &struct{}{}); err != nil {
			return nil, nil, err
		}
		models, fallback := ag.ListModels()
		return modelListResult{Data: models, Default: fallback}, nil, nil
	case methodThreadStart:
		var p threadStartParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		t, err := ag.StartThread(p.WorkspaceID, p.PermissionSettings)
		if err != nil {
			return nil, nil, serverError(err)
		}
		result := threadResult{Thread: t}
		return result, func() { c.rpc.Notify(notifyThreadStarted, result) }, nil

	case methodThreadResume, methodThreadRead:
		var p threadPageParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if p.Limit == 0 {
			p.Limit = defaultItems
		}
		if p.Limit < 1 || p.Limit > maxItems || p.BeforeSeq < 0 {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d and beforeSeq not negative", maxItems)
		}
		c.s.deliver.Lock()
		view, more, err := ag.ReadThreadPage(p.ThreadID, p.BeforeSeq, p.Limit)
		if err != nil {
			c.s.deliver.Unlock()
			return nil, nil, serverError(err)
		}
		size := 0
		for i := len(view.Items) - 1; i >= 0; i-- {
			size += itemBytes(view.Items[i])
			if size > pageBytes && i < len(view.Items)-1 {
				view.Items, more = view.Items[i+1:], true
				break
			}
		}
		result := threadPageResult{ThreadView: view, HasMore: more}
		after := func() {
			c.s.deliver.Unlock()
			if method == methodThreadResume {
				for _, r := range ag.PendingRequests(p.ThreadID) {
					c.request(r.Thread.ID, r.Item)
				}
			}
		}
		return result, after, nil

	case methodThreadPermissions:
		var p struct {
			ThreadID string `json:"threadId"`
			repo.PermissionSettings
		}
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		t, err := ag.UpdateThreadSettings(p.ThreadID, p.PermissionSettings)
		if err != nil {
			return nil, nil, serverError(err)
		}
		return threadResult{Thread: t}, nil, nil

	case methodThreadList:
		var p repo.ThreadFilter
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if p.Limit == 0 {
			p.Limit = defaultLimit
		}
		if p.Limit < 1 || p.Limit > maxLimit {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
		}
		threads, err := ag.ListThreads(p)
		if err != nil {
			return nil, nil, serverError(err)
		}
		return threadListResult{Data: threads}, nil, nil

	case methodTurnStart:
		var p turnStartParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		text, rpcErr := inputText(p.Input)
		if rpcErr != nil {
			return nil, nil, rpcErr
		}
		turn, err := ag.StartTurn(p.ThreadID, agent.Input{Text: text, Connection: p.Connection})
		if err != nil {
			return nil, nil, serverError(err)
		}
		return turnResult{Turn: turn}, nil, nil

	case methodTurnSteer:
		var p turnSteerParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if p.ExpectedTurnID == "" {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
		}
		text, rpcErr := inputText(p.Input)
		if rpcErr != nil {
			return nil, nil, rpcErr
		}
		turn, err := ag.SteerTurn(p.ThreadID, p.ExpectedTurnID, text)
		if err != nil {
			return nil, nil, serverError(err)
		}
		return turnSteerResult{TurnID: turn.ID}, nil, nil

	case methodTurnInterrupt:
		var p turnInterruptParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(p.TurnID) == "" {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "turnId is required")
		}
		if err := ag.InterruptTurn(p.ThreadID, p.TurnID); err != nil {
			return nil, nil, serverError(err)
		}
		return struct{}{}, nil, nil

	case methodToolCall:
		var p toolCallParams
		if err := jsonrpc.DecodeParams(params, &p); err != nil {
			return nil, nil, err
		}
		return jsonrpc.Async(func(ctx context.Context) (any, *jsonrpc.Error) {
			result, err := ag.CallTool(ctx, p.ThreadID, p.Tool, p.Arguments)
			if err != nil {
				return nil, serverError(err)
			}
			if result.Err != nil {
				if errors.Is(result.Err, tools.ErrUnknownTool) || errors.Is(result.Err, tools.ErrInvalidArguments) {
					return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", result.Err)
				}
				return nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "%v", result.Err)
			}
			return toolCallResult{Output: result.Output}, nil
		}), nil, nil

	default:
		c.s.mu.Lock()
		m, ok := c.s.methods[method]
		c.s.mu.Unlock()
		if ok {
			return callMethod(m, params)
		}
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "method %q not found", method)
	}
}

func (c *conn) event(e agent.Event) {
	switch e.Kind {
	case agent.EventThreadStarted:
		c.rpc.Notify(notifyThreadStarted, threadResult{Thread: e.Thread})
	case agent.EventTurnQueued:
		c.rpc.Notify(notifyTurnQueued, turnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventTurnStarted:
		c.rpc.Notify(notifyTurnStarted, turnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventItemStarted:
		c.rpc.Notify(notifyItemStarted, itemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: *e.Item})
	case agent.EventItemCompleted:
		c.rpc.Notify(notifyItemCompleted, itemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: *e.Item})
	case agent.EventItemDelta:
		method := notifyAgentMessageDelta
		if e.Item.Type == repo.ItemReasoning {
			method = notifyReasoningDelta
		}
		c.rpc.Notify(method, deltaNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, ItemID: e.Item.ID, Delta: e.Delta})
	case agent.EventTurnCompleted:
		c.rpc.Notify(notifyTurnCompleted, turnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventError:
		c.rpc.Notify(notifyError, errorNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Message: e.Error})
	case agent.EventThreadStatusChanged:
		c.rpc.Notify(notifyThreadStatusChanged, threadStatusNotification{ThreadID: e.Thread.ID, Status: e.Status})
	case agent.EventApprovalRequested, agent.EventUserInputRequested:
		c.request(e.Thread.ID, *e.Item)
	}
}

func (c *conn) request(threadID string, it repo.ItemRow) {
	c.mu.Lock()
	if c.asked[it.ID] {
		c.mu.Unlock()
		return
	}
	c.asked[it.ID] = true
	c.mu.Unlock()
	answered := func(result json.RawMessage, rerr *jsonrpc.Error) {
		c.mu.Lock()
		delete(c.asked, it.ID)
		c.mu.Unlock()
		if rerr != nil {
			log.Printf("gateway: client refused request for item %s: %v", it.ID, rerr)
			return
		}
		if err := c.resolve(threadID, it, result); err != nil {
			log.Printf("gateway: response for item %s: %v", it.ID, err)
			c.rpc.Notify(notifyError, errorNotification{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Message: err.Error()})
			if !errors.Is(err, agent.ErrNoSuchRequest) {
				c.request(threadID, it)
			}
		}
	}
	if !it.WaitsForApproval() {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			log.Printf("gateway: item %s: %v", it.ID, err)
			return
		}
		c.rpc.Request(requestUserInput, userInputRequestParams{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Questions: req.Questions}, answered)
		return
	}
	args := json.RawMessage(it.Arguments)
	if strings.TrimSpace(it.Arguments) == "" {
		args = json.RawMessage("{}")
	}
	c.rpc.Request(requestApproval, approvalRequestParams{
		ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, ApprovalID: it.Approval.ID,
		Server: it.Server, Tool: it.Tool, Arguments: args, PreparedExecution: it.PreparedExecution,
		Reason: it.Approval.Reason, AvailableDecisions: it.Approval.AvailableDecisions(),
	}, answered)
}

func (c *conn) resolve(threadID string, it repo.ItemRow, result json.RawMessage) error {
	if !it.WaitsForApproval() {
		var r userInputResponse
		if err := json.Unmarshal(result, &r); err != nil {
			return err
		}
		return c.s.agent.AnswerUserInput(threadID, it.ID, r.Answers)
	}
	var r approvalResponse
	if err := json.Unmarshal(result, &r); err != nil {
		return err
	}
	return c.s.agent.ResolveApproval(threadID, it.Approval.ID, r.Decision, "gateway")
}

const pageBytes = 4 << 20

func itemBytes(it repo.ItemRow) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return pageBytes
	}
	return len(raw)
}

func inputText(input []userInput) (string, *jsonrpc.Error) {
	texts := make([]string, 0, len(input))
	for _, in := range input {
		if in.Type != "text" {
			return "", jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "input type %q is not supported", in.Type)
		}
		texts = append(texts, in.Text)
	}
	return strings.Join(texts, "\n"), nil
}

var errorCodes = []struct {
	err  error
	code int
}{
	{repo.ErrNotFound, errNotFound},
	{agent.ErrNoActiveTurn, errNoActiveTurn},
	{agent.ErrTurnMismatch, errTurnMismatch},
	{agent.ErrNoSuchRequest, errNoSuchRequest},
	{agent.ErrSubagentThread, errSubagentThread},
	{agent.ErrWorkspaceArchived, errWorkspaceArchived},
	{agent.ErrInvalidMessage, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidAnswer, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidDecision, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidName, jsonrpc.CodeInvalidParams},
	{permission.ErrInvalidSettings, jsonrpc.CodeInvalidParams},
	{tools.ErrUnknownTool, jsonrpc.CodeInvalidParams},
	{tools.ErrInvalidArguments, jsonrpc.CodeInvalidParams},
}

func serverError(err error) *jsonrpc.Error {
	for _, e := range errorCodes {
		if errors.Is(err, e.err) {
			return jsonrpc.Errorf(e.code, "%v", err)
		}
	}
	return jsonrpc.Errorf(jsonrpc.CodeServerError, "%v", err)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

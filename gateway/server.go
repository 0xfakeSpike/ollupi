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
	"github.com/0xfakeSpike/ollupi/tools/cron"
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
	jobs    *cron.Jobs
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
	if _, ok := protocol.clientNotifications[method]; !ok {
		log.Printf("gateway: ignored client notification %q", method)
	}
}

func (c *conn) Call(method string, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	c.mu.Lock()
	initialized := c.initialized
	c.mu.Unlock()
	switch {
	case method == methodInitialize && initialized:
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "already initialized")
	case method != methodInitialize && !initialized:
		return nil, nil, jsonrpc.Errorf(errNotInitialized, "call initialize first")
	}
	if m, ok := protocol.methods[method]; ok {
		return m.call(c, params)
	}
	c.s.mu.Lock()
	m, ok := c.s.methods[method]
	c.s.mu.Unlock()
	if !ok {
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "method %q not found", method)
	}
	return callMethod(m, params)
}

func (c *conn) initialize(InitializeParams) (InitializeResult, func(), error) {
	after := func() {
		c.mu.Lock()
		c.initialized = true
		c.mu.Unlock()
		for _, r := range c.s.agent.AllPendingRequests() {
			c.request(r.Thread.ID, r.Item)
		}
	}
	return InitializeResult{ServerInfo: ServerInfo{Name: "ollupi", Version: version()}, ProtocolSHA256: protocolSHA256}, after, nil
}

func (c *conn) modelList(Empty) (ModelListResult, func(), error) {
	models, fallback := c.s.agent.ListModels()
	return ModelListResult{Data: nonNil(models), Default: fallback}, nil, nil
}

func (c *conn) toolList(Empty) (ToolListResult, func(), error) {
	return ToolListResult{Data: nonNil(c.s.agent.ListTools())}, nil, nil
}

func (c *conn) event(e agent.Event) {
	switch e.Kind {
	case agent.EventThreadStarted:
		c.rpc.Notify(notifyThreadStarted, ThreadResult{Thread: e.Thread})
	case agent.EventTurnQueued:
		c.rpc.Notify(notifyTurnQueued, TurnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventTurnStarted:
		c.rpc.Notify(notifyTurnStarted, TurnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventItemStarted:
		c.rpc.Notify(notifyItemStarted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: *e.Item})
	case agent.EventItemCompleted:
		c.rpc.Notify(notifyItemCompleted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: *e.Item})
	case agent.EventItemDelta:
		method := notifyAgentMessageDelta
		if e.Item.Type == repo.ItemReasoning {
			method = notifyReasoningDelta
		}
		c.rpc.Notify(method, DeltaNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, ItemID: e.Item.ID, Delta: e.Delta})
	case agent.EventTurnCompleted:
		c.rpc.Notify(notifyTurnCompleted, TurnNotification{ThreadID: e.Thread.ID, Thread: e.Thread, Turn: e.Turn})
	case agent.EventError:
		c.rpc.Notify(notifyError, ErrorNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Message: e.Error})
	case agent.EventThreadStatusChanged:
		c.rpc.Notify(notifyThreadStatusChanged, ThreadStatusNotification{ThreadID: e.Thread.ID, Status: e.Status})
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
			c.rpc.Notify(notifyError, ErrorNotification{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Message: err.Error()})
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
		c.rpc.Request(requestUserInput, UserInputRequestParams{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Questions: nonNil(req.Questions)}, answered)
		return
	}
	args := json.RawMessage(it.Arguments)
	if strings.TrimSpace(it.Arguments) == "" {
		args = json.RawMessage("{}")
	}
	c.rpc.Request(requestApproval, ApprovalRequestParams{
		ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, ApprovalID: it.Approval.ID,
		Server: it.Server, Tool: it.Tool, Arguments: args, PreparedExecution: it.PreparedExecution,
		Reason: it.Approval.Reason, AvailableDecisions: it.Approval.AvailableDecisions(),
	}, answered)
}

func (c *conn) resolve(threadID string, it repo.ItemRow, result json.RawMessage) error {
	if !it.WaitsForApproval() {
		var r UserInputResponse
		if err := json.Unmarshal(result, &r); err != nil {
			return err
		}
		return c.s.agent.AnswerUserInput(threadID, it.ID, r.Answers)
	}
	var r ApprovalResponse
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

func inputText(input []UserInput) (string, error) {
	texts := make([]string, 0, len(input))
	for _, in := range input {
		if in.Type != "text" {
			return "", jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "input type %q is not supported", in.Type)
		}
		texts = append(texts, in.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// nonNil returns s, or an empty slice for nil: the lists protocol.schema.json
// declares as arrays are never sent as null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
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
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) && rpcErr != nil {
		return rpcErr
	}
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

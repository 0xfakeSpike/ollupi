package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/coder/websocket"
)

const (
	maxMessageBytes = 4 << 20
	defaultLimit    = 20
	maxLimit        = 100
	defaultItems    = 100
	maxItems        = 500
)

type Server struct {
	app          *app.App
	feed         *store.Feed
	authenticate auth.Authenticate
	admit        func(ctx context.Context, user string) error
	hosts        []string
	origins      []string
	hub          *wshub.Hub
	ready        sync.Map
	served       sync.WaitGroup
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == auth.HealthPath {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.served.Add(1)
	defer s.served.Done()
	user, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if !s.hub.Admit(w) {
		return
	}
	if _, ok := s.ready.Load(user); !ok {
		if !s.admits(w, r, user) {
			return
		}
		if err := s.app.EnsureUser(r.Context(), user); err != nil {
			http.Error(w, "the user could not be made ready", http.StatusInternalServerError)
			return
		}
		s.ready.Store(user, struct{}{})
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins, Subprotocols: []string{auth.TokenProtocol}})
	if err != nil {
		return
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{s: s, user: user, ws: ws, rpc: jsonrpc.NewConn(ws), asked: make(map[string]bool)}
	s.hub.Serve(r.Context(), user, ws, c.rpc.Silent, func(ctx context.Context) error { return c.rpc.Serve(ctx, c.Call) })
	c.mu.Lock()
	sub := c.sub
	c.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

type conn struct {
	user string
	s    *Server
	ws   *websocket.Conn
	rpc  *jsonrpc.Conn

	mu          sync.Mutex
	initialized bool
	sub         *store.Subscription
	asked       map[string]bool
}

func (c *conn) Call(ctx context.Context, method string, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	c.mu.Lock()
	initialized := c.initialized
	c.mu.Unlock()
	switch {
	case method == methodInitialize && initialized:
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "already initialized")
	case method != methodInitialize && !initialized:
		return nil, nil, jsonrpc.Errorf(errNotInitialized, "call initialize first")
	}
	m, ok := protocol.methods[method]
	if !ok {
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "method %q not found", method)
	}
	return m.call(c, ctx, params)
}

func (c *conn) initialize(ctx context.Context, _ InitializeParams) (InitializeResult, func(), error) {
	after := func() {
		sub := c.s.feed.Subscribe(c.user, c.event)
		c.mu.Lock()
		c.initialized, c.sub = true, sub
		c.mu.Unlock()
		c.requests(c.s.app.Requests(context.Background(), c.user, ""))
	}
	return InitializeResult{ServerInfo: ServerInfo{Name: "ollupi", Version: version()}, ProtocolSHA256: protocolSHA256}, after, nil
}

func (c *conn) event(e store.Event) {
	switch {
	case e.Delta != nil:
		d := e.Delta
		method, text := notifyAgentMessageDelta, d.Text
		if d.Reasoning != "" {
			method, text = notifyReasoningDelta, d.Reasoning
		}
		c.rpc.Notify(method, DeltaNotification{ThreadID: e.Thread, TurnID: d.Turn, ItemID: d.Item, Delta: text})
	case e.Change != nil:
		c.change(e.Seq, e.Thread, *e.Change)
	case e.Owned != nil:
		c.owned(e.Thread, *e.Owned)
	}
}

func (c *conn) change(seq int64, thread string, ch store.Change) {
	if ch.Deleted {
		c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: thread, Seq: seq})
		return
	}
	header := threadRow(*ch.Header)
	c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: thread, Thread: &header, Seq: seq})
	for _, t := range ch.Turns {
		c.rpc.Notify(notifyTurnChanged, TurnChangedNotification{ThreadID: thread, Turn: turnRow(thread, t, t.ID == ch.Active), Seq: seq})
	}
	for _, it := range ch.Items {
		c.rpc.Notify(notifyItemChanged, ItemChangedNotification{ThreadID: thread, Item: itemRow(it), Seq: seq})
		if it.WaitsForApproval() || it.WaitsForInput() {
			c.request(thread, it)
		}
	}
}

func (c *conn) owned(thread string, o store.Owned) {
	switch {
	case o.Settings != nil:
		c.rpc.Notify(notifySettingsChanged, settingsRow(*o.Settings))
	case o.Workspace != nil:
		c.rpc.Notify(notifyWorkspaceChanged, WorkspaceResult{Workspace: workspaceRow(*o.Workspace)})
	case o.Environment != nil:
		row := environmentRow(*o.Environment)
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: row.ID, Environment: &row})
	case o.EnvironmentDeleted != "":
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: o.EnvironmentDeleted})
	case o.MCPServer != nil:
		server := mcpServerConfig(*o.MCPServer)
		c.rpc.Notify(notifyMCPServerChanged, MCPServerChangedNotification{Name: server.Name, Server: &server})
	case o.MCPServerDeleted != "":
		c.rpc.Notify(notifyMCPServerChanged, MCPServerChangedNotification{Name: o.MCPServerDeleted})
	case o.CronJob != nil:
		row := cronJobRow(*o.CronJob)
		c.rpc.Notify(notifyCronChanged, CronChangedNotification{ID: row.ID, ThreadID: thread, Job: &row})
	case o.CronJobDeleted != "":
		c.rpc.Notify(notifyCronChanged, CronChangedNotification{ID: o.CronJobDeleted, ThreadID: thread})
	case o.Memory != nil:
		c.rpc.Notify(notifyMemoryChanged, MemoryResult{Memory: memoryRow(*o.Memory)})
	}
}

func (c *conn) requests(pending []app.Request, err error) {
	if err != nil {
		go c.ws.Close(websocket.StatusInternalError, "pending requests could not be read")
		return
	}
	for _, r := range pending {
		c.request(r.Thread.ID, r.Item)
	}
}

func (c *conn) request(thread string, it conversation.Item) {
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
			return
		}
		if err := c.resolve(thread, it, result); err != nil {
			c.rpc.Notify(notifyError, ErrorNotification{ThreadID: thread, TurnID: it.Turn, ItemID: it.ID, Message: serverError(err).Message})
			if !errors.Is(err, app.ErrNoSuchRequest) {
				c.request(thread, it)
			}
		}
	}
	if it.Approval == nil {
		req, err := ask.Decode(it.Input.Request)
		if err != nil {
			return
		}
		c.rpc.Request(requestUserInput, UserInputRequestParams{ThreadID: thread, TurnID: it.Turn, ItemID: it.ID, Questions: nonNil(req.Questions)}, answered)
		return
	}
	args := json.RawMessage(it.Call.Arguments)
	if strings.TrimSpace(it.Call.Arguments) == "" {
		args = json.RawMessage("{}")
	}
	a := it.Approval
	c.rpc.Request(requestApproval, ApprovalRequestParams{
		ThreadID: thread, TurnID: it.Turn, ItemID: it.ID, ApprovalID: a.Approval, Server: it.Call.Server, Tool: it.Call.Tool,
		Arguments: args, PreparedExecution: it.Call.Prepared, Reason: a.Reason, Content: a.Content, SuggestedRules: a.Suggested,
		AvailableDecisions: a.Decisions(),
	}, answered)
}

func (c *conn) resolve(thread string, it conversation.Item, result json.RawMessage) error {
	ctx := context.Background()
	if it.Approval == nil {
		var r UserInputResponse
		if err := jsonrpc.DecodeParams(result, &r); err != nil {
			return err
		}
		if r.Skipped {
			r.Answers = nil
		}
		return c.s.app.Answer(ctx, c.user, thread, it.ID, r.Answers)
	}
	var r ApprovalResponse
	if err := jsonrpc.DecodeParams(result, &r); err != nil {
		return err
	}
	return c.s.app.Decide(ctx, c.user, thread, it.Approval.Approval, r.Decision)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

func (s *Server) admits(w http.ResponseWriter, r *http.Request, user string) bool {
	if s.admit == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	if err := s.admit(ctx, user); err != nil {
		s.hub.Refuse(w, "this user is not served here now; try again")
		return false
	}
	return true
}

func (s *Server) Connections() int { return s.hub.Count() }

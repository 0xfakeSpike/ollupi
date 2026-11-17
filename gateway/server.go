package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime/debug"
	"sync"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/coder/websocket"
)

// Requests are traced through the global TracerProvider, which the host sets up.

const (
	// The largest message a client has reason to send is one user message, held to a quarter of a model's input
	// (service.checkLength): files go by path. A megabyte is about that for a million tokens of input.
	maxMessageBytes = 4 << 20
	defaultLimit    = 20
	maxLimit        = 100
	defaultItems    = 100
	maxItems        = 500
)

type Server struct {
	service      *service.Service
	events       *event.Bus
	authenticate Authenticate
	shard        string
	placements   Placements
	load         Load
	hosts        []string
	origins      []string
	hub          *wshub.Hub[*conn]
	// ready holds the users found on this shard and made ready by EnsureUser, so that their reconnections ask and write
	// nothing: a user stays on their shard, and none is removed.
	ready  sync.Map
	served sync.WaitGroup
}

// HealthPath answers 200 while the process serves, for a load balancer's health check: a shard serves only once it
// holds its lease, so the check sends a shard's connections to the process that holds it.
const HealthPath = "/healthz"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == HealthPath {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.served.Add(1)
	defer s.served.Done()
	user, status, why := s.authorize(r)
	switch status {
	case 0:
	case http.StatusServiceUnavailable:
		s.hub.Refuse(w, why)
		return
	default:
		http.Error(w, why, status)
		return
	}
	if !s.hub.Admit(w) {
		return
	}
	if _, ok := s.ready.Load(user); !ok {
		if !s.mine(w, r, user) {
			return
		}
		if err := s.service.EnsureUser(user); err != nil {
			http.Error(w, "the user could not be made ready", http.StatusInternalServerError)
			return
		}
		s.ready.Store(user, struct{}{})
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins, Subprotocols: []string{tokenProtocol}})
	if err != nil {
		return
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{s: s, user: user, ws: ws, rpc: jsonrpc.NewConn(ws), asked: make(map[string]bool)}
	s.hub.Serve(r.Context(), user, ws, c, func(ctx context.Context) error { return c.rpc.Serve(ctx, c) })
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
	sub         *event.Subscription
	asked       map[string]bool
}

// Notify takes a client's notification, which asks for no answer and changes nothing on the shard.
func (c *conn) Notify(string, json.RawMessage) {}

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
	// What is published from here on reaches the client; what waited on them before is asked again, which
	// c.request does not ask twice.
	after := func() {
		sub := c.s.events.Subscribe(c.user, c.event)
		c.mu.Lock()
		c.initialized, c.sub = true, sub
		c.mu.Unlock()
		c.requests(c.s.service.AllPendingRequests(c.user))
	}
	return InitializeResult{ServerInfo: ServerInfo{Name: "ollupi", Version: version()}, ProtocolSHA256: protocolSHA256}, after, nil
}

func (c *conn) event(e event.Event) {
	switch e.Kind {
	case event.ThreadChanged:
		changed := threadRow(e.Thread)
		c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: e.Thread.ID, Thread: &changed})
	case event.ThreadDeleted:
		c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: e.Thread.ID})
	case event.TurnQueued:
		c.rpc.Notify(notifyTurnQueued, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case event.TurnStarted:
		c.rpc.Notify(notifyTurnStarted, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case event.ItemStarted:
		c.rpc.Notify(notifyItemStarted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case event.ItemUpdated:
		c.rpc.Notify(notifyItemUpdated, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case event.ItemCompleted:
		c.rpc.Notify(notifyItemCompleted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case event.ItemDelta:
		method := notifyAgentMessageDelta
		if e.Item.Type == db.ItemReasoning {
			method = notifyReasoningDelta
		}
		c.rpc.Notify(method, DeltaNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, ItemID: e.Item.ID, Delta: e.Delta})
	case event.TurnCompleted:
		c.rpc.Notify(notifyTurnCompleted, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case event.Error:
		c.rpc.Notify(notifyError, ErrorNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Message: e.Error})
	case event.ThreadStatusChanged:
		c.rpc.Notify(notifyThreadStatusChanged, ThreadStatusNotification{ThreadID: e.Thread.ID, Status: e.Status})
	case event.WorkspaceChanged:
		c.rpc.Notify(notifyWorkspaceChanged, WorkspaceResult{Workspace: workspaceRow(*e.Workspace)})
	case event.EnvironmentChanged:
		row := environmentRow(*e.Environment)
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: e.Environment.ID, Environment: &row})
	case event.EnvironmentDeleted:
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: e.Environment.ID})
	case event.CronJobChanged:
		n := CronChangedNotification{ID: e.CronJobID, ThreadID: e.Thread.ID}
		if e.CronJob != nil {
			view := cronJobView(*e.CronJob)
			n.Job = &view
		}
		c.rpc.Notify(notifyCronChanged, n)
	case event.SettingsChanged:
		c.rpc.Notify(notifySettingsChanged, settingsRow(*e.Settings))
	case event.ApprovalRequested, event.UserInputRequested:
		c.request(e.Thread.ID, *e.Item)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

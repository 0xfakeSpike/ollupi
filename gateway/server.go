package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
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
	welcome      func(ctx context.Context, user string) error
	admit        func(ctx context.Context, user string) error
	hosts        []string
	origins      []string
	hub          *wshub.Hub
	handler      http.Handler
	ready        sync.Map
	served       sync.WaitGroup
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	s.served.Add(1)
	defer s.served.Done()
	id, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if !s.hub.Admit(w) {
		return
	}
	if !s.serves(w, r, id.User) {
		return
	}
	user := id.User
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins, Subprotocols: []string{auth.TokenProtocol}})
	if err != nil {
		return
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{s: s, user: user, operator: id.Operator, ws: ws, rpc: jsonrpc.NewConn(ws), asked: make(map[string]bool)}
	s.hub.Serve(r.Context(), user, ws, c.rpc.Silent, func(ctx context.Context) error { return c.rpc.Serve(ctx, c.Call, concurrent) })
	c.mu.Lock()
	sub := c.sub
	c.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

type conn struct {
	user string
	// operator is a connection of the deployment's operator, which may read
	// and change the catalog of models.
	operator bool
	s        *Server
	ws       *websocket.Conn
	rpc      *jsonrpc.Conn

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
	if m.operator && !c.operator {
		return nil, nil, jsonrpc.Errorf(errForbidden, "%s is the deployment operator's; this connection acts as a user", method)
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
		c.rpc.Notify(method, DeltaNotification{ThreadID: e.Thread, TurnID: d.Turn, ItemID: d.Item, Delta: text, Offset: d.Offset})
	case e.Change != nil:
		c.change(e.Seq, e.Thread, *e.Change)
	case e.Owned != nil:
		c.owned(e.Seq, e.Thread, *e.Owned)
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

func (c *conn) owned(seq int64, thread string, o store.Owned) {
	switch {
	case o.Settings != nil:
		c.rpc.Notify(notifySettingsChanged, SettingsChangedNotification{SettingsRow: settingsRow(*o.Settings), Seq: seq})
	case o.Workspace != nil:
		c.rpc.Notify(notifyWorkspaceChanged, WorkspaceChangedNotification{Workspace: workspaceRow(*o.Workspace), Seq: seq})
	case o.Environment != nil:
		row := environmentRow(*o.Environment)
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: row.ID, Environment: &row, Seq: seq})
	case o.EnvironmentDeleted != "":
		c.rpc.Notify(notifyEnvironmentChanged, EnvironmentChangedNotification{EnvironmentID: o.EnvironmentDeleted, Seq: seq})
	case o.MCPServer != nil:
		server := mcpServerConfig(*o.MCPServer)
		c.rpc.Notify(notifyMCPServerChanged, MCPServerChangedNotification{Name: server.Name, Server: &server, Seq: seq})
	case o.MCPServerDeleted != "":
		c.rpc.Notify(notifyMCPServerChanged, MCPServerChangedNotification{Name: o.MCPServerDeleted, Seq: seq})
	case o.CronJob != nil:
		row := cronJobRow(*o.CronJob)
		c.rpc.Notify(notifyCronChanged, CronChangedNotification{ID: row.ID, ThreadID: thread, Job: &row, Seq: seq})
	case o.CronJobDeleted != "":
		c.rpc.Notify(notifyCronChanged, CronChangedNotification{ID: o.CronJobDeleted, ThreadID: thread, Seq: seq})
	case o.Memory != nil:
		c.rpc.Notify(notifyMemoryChanged, MemoryChangedNotification{Memory: memoryRow(*o.Memory), Seq: seq})
	case o.Todo != nil:
		c.rpc.Notify(notifyTodoChanged, TodoChangedNotification{Todo: todoRow(*o.Todo), Seq: seq})
	case o.Skill != nil:
		row := skillRow(*o.Skill)
		c.rpc.Notify(notifySkillChanged, SkillChangedNotification{Name: row.Name, Skill: &row, Seq: seq})
	case o.SkillDeleted != "":
		c.rpc.Notify(notifySkillChanged, SkillChangedNotification{Name: o.SkillDeleted, Seq: seq})
	case o.Catalog != nil:
		c.catalogChanged(seq, *o.Catalog)
	case o.User != nil:
		// The operator hears of every user; a user only of themself.
		if c.operator || o.User.ID == c.user {
			c.rpc.Notify(notifyUserChanged, UserChangedNotification{User: userRow(*o.User), Seq: seq})
		}
	}
}

// catalogChanged tells the operator the catalog, and every connection the
// models its user now chooses among.
func (c *conn) catalogChanged(seq int64, catalog []account.ModelProvider) {
	if c.operator {
		c.rpc.Notify(notifyCatalogChanged, CatalogChangedNotification{Data: each(catalog, providerRow), Seq: seq})
	}
	s, _, err := c.s.app.Settings(context.Background(), c.user)
	if err != nil {
		return
	}
	models, first := account.Models(catalog, s)
	if models == nil {
		models = []llm.Model{}
	}
	c.rpc.Notify(notifyModelChanged, ModelChangedNotification{Data: models, Default: first, Seq: seq})
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
		Escalated: a.Escalated, AvailableDecisions: a.Decisions(),
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

func (s *Server) serves(w http.ResponseWriter, r *http.Request, user string) bool {
	if _, ok := s.ready.Load(user); ok || s.admit == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	if err := s.admit(ctx, user); err != nil {
		s.hub.Refuse(w, "this user is not served here now; try again")
		return false
	}
	s.ready.Store(user, struct{}{})
	return true
}

func (s *Server) Connections() int { return s.hub.Count() }

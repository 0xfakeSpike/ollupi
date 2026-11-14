package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Requests are traced through the global TracerProvider, which the host sets up.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/gateway")

const (
	maxMessageBytes = 32 << 20
	defaultLimit    = 20
	maxLimit        = 100
	defaultItems    = 100
	maxItems        = 500
)

type Server struct {
	orchestrator *agent.Orchestrator
	db           *repo.DB
	memory       *memory.Manager
	jobs         *cron.Jobs
	mcp          *mcp.Users
	authenticate func(token string) (string, bool)
	shard        string
	placements   Placements
	load         Load
	hosts        []string
	origins      []string
	// hub holds the users' connections, keyed by user.
	hub *wshub.Hub[*conn]
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
	if status != 0 {
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
		if err := s.db.EnsureUser(user); err != nil {
			slog.Error("user not ready", "user", user, "err", err)
			http.Error(w, "the user could not be made ready", http.StatusInternalServerError)
			return
		}
		s.ready.Store(user, struct{}{})
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins, Subprotocols: []string{tokenProtocol}})
	if err != nil {
		slog.Warn("client not accepted", "err", err)
		return
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{s: s, user: user, ws: ws, rpc: jsonrpc.NewConn(ws), asked: make(map[string]bool)}
	slog.Info("client connected", "origin", r.Header.Get("Origin"))
	s.hub.Serve(r.Context(), user, ws, c, func(ctx context.Context) error { return c.rpc.Serve(ctx, c) })
}

// mine says whether user is on this shard, and when not, refuses the connection for now: the router placed it by what
// it knew, and a client that comes back reaches the right shard once that is put right.
func (s *Server) mine(w http.ResponseWriter, r *http.Request, user string) bool {
	if s.placements == nil {
		return true
	}
	shard, err := s.placements.ShardOf(r.Context(), user)
	switch {
	case err != nil:
		slog.Error("user's shard not found", "user", user, "err", err)
	case shard != s.shard:
		slog.Warn("connection refused on the wrong shard", "user", user, "shard", s.shard, "theirs", shard)
	default:
		return true
	}
	s.hub.Refuse(w, "this user is not on this shard now; try again")
	return false
}

// Deliver sends an event to the connections of the user it belongs to; one that names no user goes to nobody, the
// log has it.
func (s *Server) Deliver(e agent.Event) {
	user := e.Thread.UserID
	if e.Workspace != nil {
		user = e.Workspace.UserID
	}
	if e.Environment != nil {
		user = e.Environment.UserID
	}
	switch e.Kind {
	case agent.EventEnvironmentChanged:
		s.environmentChanged(user, e.Environment.ID)
	case agent.EventCronJobChanged:
		s.cronChanged(user, e.CronJobID, e.Thread.ID)
	default:
		s.each(user, func(c *conn) { c.event(e) })
	}
}

// each calls fn with each of the user's connections that has been initialized.
func (s *Server) each(user string, fn func(*conn)) {
	s.hub.Each(user, func(c *conn) {
		if c.isInitialized() {
			fn(c)
		}
	})
}

type conn struct {
	user string
	s    *Server
	ws   *websocket.Conn
	rpc  *jsonrpc.Conn

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
		slog.Warn("client notification ignored", "method", method)
	}
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
	// Each request starts a trace of its own, linked to whatever traced the connection: a connection lasts for hours,
	// and what its client sends is not trusted to name a parent.
	ctx, span := tracer.Start(ctx, method, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithLinks(trace.LinkFromContext(ctx)), trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"), attribute.String("rpc.method", method), attribute.String("ollupi.user.id", c.user)))
	defer span.End()
	result, after, rerr := m.call(c, ctx, params)
	if rerr != nil {
		span.SetAttributes(attribute.Int("rpc.jsonrpc.error_code", rerr.Code))
		if rerr.Code == jsonrpc.CodeServerError || rerr.Code == jsonrpc.CodeInternalError {
			span.SetStatus(codes.Error, rerr.Message)
		}
	}
	return result, after, rerr
}

func (c *conn) initialize(ctx context.Context, _ InitializeParams) (InitializeResult, func(), error) {
	after := func() {
		c.mu.Lock()
		c.initialized = true
		c.mu.Unlock()
		c.requests(c.s.orchestrator.AllPendingRequests(c.user))
	}
	return InitializeResult{ServerInfo: ServerInfo{Name: "ollupi", Version: version()}, ProtocolSHA256: protocolSHA256}, after, nil
}

func (c *conn) modelList(ctx context.Context, _ Empty) (ModelListResult, func(), error) {
	models, fallback := c.s.orchestrator.ListModels()
	return ModelListResult{Data: nonNil(models), Default: fallback}, nil, nil
}

func (c *conn) toolList(ctx context.Context, _ Empty) (ToolListResult, func(), error) {
	list, err := c.s.orchestrator.ListTools(c.user)
	if err != nil {
		return ToolListResult{}, nil, err
	}
	return ToolListResult{Data: nonNil(list)}, nil, nil
}

func (c *conn) mcpServerList(ctx context.Context, _ Empty) (MCPServerListResult, func(), error) {
	var servers []mcp.ServerStatus
	if c.s.mcp != nil {
		mine, err := c.s.mcp.For(c.user)
		if err != nil {
			return MCPServerListResult{}, nil, err
		}
		servers = mine.Statuses()
	}
	return MCPServerListResult{Data: nonNil(servers)}, nil, nil
}

func (c *conn) event(e agent.Event) {
	switch e.Kind {
	case agent.EventThreadStarted:
		c.rpc.Notify(notifyThreadStarted, ThreadResult{Thread: threadRow(e.Thread)})
	case agent.EventThreadChanged:
		changed := threadRow(e.Thread)
		c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: e.Thread.ID, Thread: &changed})
	case agent.EventThreadDeleted:
		c.rpc.Notify(notifyThreadChanged, ThreadChangedNotification{ThreadID: e.Thread.ID})
	case agent.EventTurnQueued:
		c.rpc.Notify(notifyTurnQueued, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case agent.EventTurnStarted:
		c.rpc.Notify(notifyTurnStarted, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case agent.EventItemStarted:
		c.rpc.Notify(notifyItemStarted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case agent.EventItemUpdated:
		c.rpc.Notify(notifyItemUpdated, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case agent.EventItemCompleted:
		c.rpc.Notify(notifyItemCompleted, ItemNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Item: itemRow(*e.Item)})
	case agent.EventItemDelta:
		method := notifyAgentMessageDelta
		if e.Item.Type == repo.ItemReasoning {
			method = notifyReasoningDelta
		}
		c.rpc.Notify(method, DeltaNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, ItemID: e.Item.ID, Delta: e.Delta})
	case agent.EventTurnCompleted:
		c.rpc.Notify(notifyTurnCompleted, TurnNotification{ThreadID: e.Thread.ID, Thread: threadRow(e.Thread), Turn: turnRow(e.Turn)})
	case agent.EventError:
		c.rpc.Notify(notifyError, ErrorNotification{ThreadID: e.Thread.ID, TurnID: e.Turn.ID, Message: e.Error})
	case agent.EventThreadStatusChanged:
		c.rpc.Notify(notifyThreadStatusChanged, ThreadStatusNotification{ThreadID: e.Thread.ID, Status: e.Status})
	case agent.EventWorkspaceChanged:
		c.rpc.Notify(notifyWorkspaceChanged, WorkspaceResult{Workspace: workspaceRow(*e.Workspace)})
	case agent.EventApprovalRequested, agent.EventUserInputRequested:
		c.request(e.Thread.ID, *e.Item)
	}
}

// requests asks the client what is waiting on it. A client that cannot be told would wait without knowing, so one that
// could not be told is dropped and asked again when it comes back.
func (c *conn) requests(pending []agent.Request, err error) {
	if errors.Is(err, repo.ErrNotFound) {
		return
	}
	if err != nil {
		slog.Error("pending requests not read", "user", c.user, "err", err)
		go c.ws.Close(websocket.StatusInternalError, "pending requests could not be read")
		return
	}
	for _, r := range pending {
		c.request(r.Thread.ID, r.Item)
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
			slog.Warn("client refused a request", "item", it.ID, "err", rerr)
			return
		}
		if err := c.resolve(threadID, it, result); err != nil {
			slog.Warn("client response not applied", "item", it.ID, "err", err)
			c.rpc.Notify(notifyError, ErrorNotification{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Message: serverError(err).Message})
			if !errors.Is(err, agent.ErrNoSuchRequest) {
				c.request(threadID, it)
			}
		}
	}
	if !it.WaitsForApproval() {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			slog.Error("request not sent", "item", it.ID, "err", err)
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
		Reason: it.Approval.Reason, Content: it.Approval.Content, SuggestedRules: it.Approval.Suggested, AvailableDecisions: it.Approval.AvailableDecisions(),
	}, answered)
}

func (c *conn) resolve(threadID string, it repo.ItemRow, result json.RawMessage) error {
	if !it.WaitsForApproval() {
		var r UserInputResponse
		if err := json.Unmarshal(result, &r); err != nil {
			return err
		}
		if r.Skipped {
			return c.s.orchestrator.SkipUserInput(c.user, threadID, it.ID)
		}
		return c.s.orchestrator.AnswerUserInput(c.user, threadID, it.ID, r.Answers)
	}
	var r ApprovalResponse
	if err := json.Unmarshal(result, &r); err != nil {
		return err
	}
	return c.s.orchestrator.ResolveApproval(c.user, threadID, it.Approval.ID, r.Decision, "gateway")
}

const pageBytes = 4 << 20

func itemBytes(it repo.ItemRow) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return pageBytes
	}
	return len(raw)
}

func agentInput(input []UserInput) (agent.Input, error) {
	var in agent.Input
	texts := make([]string, 0, len(input))
	for _, part := range input {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "file":
			in.Attachments = append(in.Attachments, agent.AttachmentInput{Path: part.Path, Name: part.Name, MimeType: part.MimeType})
		default:
			return agent.Input{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "input type %q is not supported", part.Type)
		}
	}
	in.Text = strings.Join(texts, "\n")
	return in, nil
}

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
	{agent.ErrThreadArchived, errThreadArchived},
	{agent.ErrThreadBusy, errThreadBusy},
	{agent.ErrWorkspaceBusy, errThreadBusy},
	{agent.ErrQueueFull, errThreadBusy},
	{agent.ErrEnvironmentInUse, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidMessage, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidAnswer, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidDecision, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidName, jsonrpc.CodeInvalidParams},
	{agent.ErrInvalidPath, jsonrpc.CodeInvalidParams},
	{permission.ErrInvalidSettings, jsonrpc.CodeInvalidParams},
	{memory.ErrInvalidChange, jsonrpc.CodeInvalidParams},
	{tools.ErrUnknownTool, jsonrpc.CodeInvalidParams},
	{tools.ErrInvalidArguments, jsonrpc.CodeInvalidParams},
	{cron.ErrInvalidJob, jsonrpc.CodeInvalidParams},
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
	slog.Error("request failed", "err", err)
	return jsonrpc.Errorf(jsonrpc.CodeServerError, "the request could not be completed")
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

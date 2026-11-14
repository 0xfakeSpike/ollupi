package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
)

type Options struct {
	// DB is the orchestrator's database; the gateway reads it for views that need no orchestration.
	DB *repo.DB
	// Token is the default user's, for a gateway with one user; Authenticate takes its place for many.
	Token string
	// Authenticate says whose token this is. Logging in happens before ollupi: it only checks what that gave out.
	Authenticate   func(token string) (user string, ok bool)
	AllowedOrigins []string
	// AllowedHosts are the names clients reach this gateway by, besides the local ones.
	AllowedHosts []string
	Memory       *memory.Manager
	Jobs         *cron.Jobs
	MCP          *mcp.Users
	// Connections bounds the clients' connections; the zero value takes DefaultConnections.
	Connections wshub.Options
	// Shard is the shard this gateway serves, and Placements where it finds which users are on it. Without them the
	// gateway serves every user Authenticate knows, as where there is one shard.
	Shard      string
	Placements Placements
	// Load reports how busy this shard is to the router, for placing new users; it needs Shard.
	Load Load
}

func NewServer(a *orchestrator.Orchestrator, opts Options) (*Server, error) {
	if opts.Authenticate == nil {
		if strings.TrimSpace(opts.Token) == "" {
			return nil, errors.New("gateway: a token is required; clients send it as Authorization: Bearer <token> or, from a browser, as the subprotocol after " + tokenProtocol)
		}
		opts.Authenticate = func(token string) (string, bool) {
			return repo.DefaultUser, subtle.ConstantTimeCompare([]byte(token), []byte(opts.Token)) == 1
		}
	}
	if opts.DB == nil {
		return nil, errors.New("gateway: the orchestrator's database is required")
	}
	if (opts.Shard == "") != (opts.Placements == nil) {
		return nil, errors.New("gateway: Shard and Placements go together")
	}
	load, err := opts.Load.withDefaults()
	if err != nil {
		return nil, err
	}
	if load.Reporter != nil && opts.Shard == "" {
		return nil, errors.New("gateway: a gateway reports its load as a shard, so Load needs Shard")
	}
	if opts.Connections == (wshub.Options{}) {
		opts.Connections = DefaultConnections
	}
	hub, err := wshub.New[*conn](opts.Connections)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	s := &Server{
		orchestrator: a,
		db:           opts.DB,
		memory:       opts.Memory,
		jobs:         opts.Jobs,
		mcp:          opts.MCP,
		authenticate: opts.Authenticate,
		shard:        opts.Shard,
		placements:   opts.Placements,
		load:         load,
		hosts:        append(slices.Clone(localHosts), opts.AllowedHosts...),
		origins:      append([]string{"localhost:*", "127.0.0.1:*", "[::1]:*"}, opts.AllowedOrigins...),
		hub:          hub,
	}
	return s, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var reporting sync.WaitGroup
	defer reporting.Wait()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if s.load.Reporter != nil {
		reporting.Go(func() { s.reportLoad(ctx) })
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", s)
	srv := &http.Server{
		Handler:           mux,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("gateway: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("gateway shutdown", "err", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		slog.Error("gateway stopped", "err", err)
	}
	s.served.Wait()
	return nil
}

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 2 * time.Minute
)

// DefaultConnections bounds the WebSocket connections a gateway holds unless Options says otherwise. The admission
// rate is a guess, not yet measured: each new connection sets off database work, so a restarted shard takes its users
// back a few hundred a second. MaxAge stays under the 24 hours that Google Cloud's global external Application Load
// Balancer keeps a WebSocket open at most; a deployment behind a balancer that cuts them sooner sets it lower.
var DefaultConnections = wshub.Options{
	PerKey:      4,
	Rate:        200,
	Burst:       400,
	RetryWithin: 5 * time.Second,
	PingEvery:   30 * time.Second,
	PongWithin:  15 * time.Second,
	MaxAge:      20 * time.Hour,
}

var localHosts = []string{"localhost", "127.0.0.1", "::1"}

// tokenProtocol is the WebSocket subprotocol a browser, which cannot set Authorization, offers just before its token.
const tokenProtocol = "ollupi.token"

// TokenOf reads the token a client sent. It stays out of the URL, which access logs keep.
func TokenOf(header http.Header) string {
	if bearer, ok := strings.CutPrefix(header.Get("Authorization"), "Bearer "); ok {
		return bearer
	}
	var offered []string
	for _, header := range header.Values("Sec-WebSocket-Protocol") {
		for protocol := range strings.SplitSeq(header, ",") {
			offered = append(offered, strings.TrimSpace(protocol))
		}
	}
	if i := slices.Index(offered, tokenProtocol); i >= 0 && i+1 < len(offered) {
		return offered[i+1]
	}
	return ""
}

// authorize says whose request this is, or the status and reason to refuse it with.
func (s *Server) authorize(r *http.Request) (string, int, string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(s.hosts, strings.Trim(host, "[]")) {
		return "", http.StatusForbidden, "host not allowed"
	}
	user, ok := s.authenticate(TokenOf(r.Header))
	if !ok {
		return "", http.StatusUnauthorized, "invalid or missing gateway token"
	}
	return user, 0, ""
}

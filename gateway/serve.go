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
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
)

type Options struct {
	// DB is the agent's database; the gateway reads it for views that need no agent logic.
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
}

func NewServer(a *agent.Agent, opts Options) (*Server, error) {
	if opts.Authenticate == nil {
		if strings.TrimSpace(opts.Token) == "" {
			return nil, errors.New("gateway: a token is required; clients send it as Authorization: Bearer <token> or, from a browser, as the subprotocol after " + tokenProtocol)
		}
		opts.Authenticate = func(token string) (string, bool) {
			return repo.DefaultUser, subtle.ConstantTimeCompare([]byte(token), []byte(opts.Token)) == 1
		}
	}
	if opts.DB == nil {
		return nil, errors.New("gateway: the agent's database is required")
	}
	if opts.Connections == (wshub.Options{}) {
		opts.Connections = DefaultConnections
	}
	hub, err := wshub.New[*conn](opts.Connections)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	s := &Server{
		agent:        a,
		db:           opts.DB,
		memory:       opts.Memory,
		jobs:         opts.Jobs,
		mcp:          opts.MCP,
		authenticate: opts.Authenticate,
		hosts:        append(slices.Clone(localHosts), opts.AllowedHosts...),
		origins:      append([]string{"localhost:*", "127.0.0.1:*", "[::1]:*"}, opts.AllowedOrigins...),
		hub:          hub,
	}
	return s, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
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

// authorize says whose request this is, or the status and reason to refuse it with.
func (s *Server) authorize(r *http.Request) (string, int, string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(s.hosts, strings.Trim(host, "[]")) {
		return "", http.StatusForbidden, "host not allowed"
	}
	user, ok := s.authenticate(tokenOf(r.Header))
	if !ok {
		return "", http.StatusUnauthorized, "invalid or missing gateway token"
	}
	return user, 0, ""
}

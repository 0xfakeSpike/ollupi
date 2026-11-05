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
}

func NewServer(a *agent.Agent, opts Options) (*Server, error) {
	if opts.Authenticate == nil {
		if strings.TrimSpace(opts.Token) == "" {
			return nil, errors.New("gateway: a token is required; clients send it as Authorization: Bearer <token>")
		}
		opts.Authenticate = func(token string) (string, bool) {
			return repo.DefaultUser, subtle.ConstantTimeCompare([]byte(token), []byte(opts.Token)) == 1
		}
	}
	if opts.DB == nil {
		return nil, errors.New("gateway: the agent's database is required")
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
		clients:      map[string]*conn{},
	}
	return s, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.Handle("GET /", s)
	srv := &http.Server{Handler: mux, BaseContext: func(net.Listener) context.Context { return ctx }}
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
	got := r.URL.Query().Get("token")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = bearer
	}
	user, ok := s.authenticate(got)
	if !ok {
		return "", http.StatusUnauthorized, "invalid or missing gateway token"
	}
	return user, 0, ""
}

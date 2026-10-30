package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
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
	DB             *repo.DB
	Token          string
	AllowedOrigins []string
	Memory         *memory.Manager
	Jobs           *cron.Jobs
	MCP            *mcp.Servers
}

func NewServer(a *agent.Agent, opts Options) (*Server, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("gateway: a token is required; clients send it as Authorization: Bearer <token>")
	}
	if opts.DB == nil {
		return nil, errors.New("gateway: the agent's database is required")
	}
	s := &Server{
		agent:   a,
		db:      opts.DB,
		memory:  opts.Memory,
		jobs:    opts.Jobs,
		mcp:     opts.MCP,
		token:   opts.Token,
		origins: append([]string{"localhost:*", "127.0.0.1:*", "[::1]:*"}, opts.AllowedOrigins...),
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
		log.Printf("gateway shutdown: %v", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		log.Printf("gateway: %v", err)
	}
	s.served.Wait()
	return nil
}

var localHosts = []string{"localhost", "127.0.0.1", "::1"}

func (s *Server) authorize(r *http.Request) (int, string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(localHosts, strings.Trim(host, "[]")) {
		return http.StatusForbidden, "host not allowed"
	}
	got := r.URL.Query().Get("token")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = bearer
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		return http.StatusUnauthorized, "invalid or missing gateway token"
	}
	return 0, ""
}

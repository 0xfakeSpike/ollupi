package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

const lookupTimeout = 10 * time.Second

type Options struct {
	// Token is the default user's, for a gateway with one user; Authenticate takes its place for many.
	Token string
	// Logging in happens before ollupi: Authenticate only checks what that gave out.
	Authenticate   Authenticate
	AllowedOrigins []string
	// AllowedHosts are the names clients reach this gateway by, besides the local ones.
	AllowedHosts []string
	Connections  wshub.Options
	// Without Shard and Placements the gateway serves every user Authenticate knows, as where there is one shard.
	Shard      string
	Placements Placements
	// Load needs Shard.
	Load Load
}

func NewServer(a *service.Service, events *event.Bus, opts Options) (*Server, error) {
	if opts.Authenticate == nil {
		if strings.TrimSpace(opts.Token) == "" {
			return nil, errors.New("gateway: a token is required; clients send it as Authorization: Bearer <token> or, from a browser, as the subprotocol after " + tokenProtocol)
		}
		opts.Authenticate = func(_ context.Context, token string) (string, error) {
			if subtle.ConstantTimeCompare([]byte(token), []byte(opts.Token)) != 1 {
				return "", ErrUnknownToken
			}
			return db.DefaultUser, nil
		}
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
		service:      a,
		events:       events,
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
	srv.Shutdown(shutdownCtx)
	<-served
	s.served.Wait()
	return nil
}

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 2 * time.Minute
)

// The admission rate is a guess, not yet measured: each new connection sets off database work. MaxAge stays under the
// 24 hours that Google Cloud's global external Application Load Balancer keeps a WebSocket open at most.
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

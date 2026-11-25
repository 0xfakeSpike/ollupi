package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/rs/cors"
)

const lookupTimeout = 10 * time.Second

type Options struct {
	// Authenticate names the user a connection acts as. It is required: no
	// connection works without a user, and none is made up for it.
	Authenticate auth.Authenticate
	// Welcome, when set, gives a user the operator just made what the
	// deployment starts everyone with, before user/create answers.
	Welcome        func(ctx context.Context, user string) error
	AllowedOrigins []string
	AllowedHosts   []string
	Connections    wshub.Options
	Admit          func(ctx context.Context, user string) error
}

func NewServer(a *app.App, feed *store.Feed, opts Options) (*Server, error) {
	if opts.Authenticate == nil {
		return nil, errors.New("gateway: Authenticate is required: every connection names its user; clients send the token as Authorization: Bearer <token> or, from a browser, as the subprotocol after " + auth.TokenProtocol)
	}
	if opts.Connections == (wshub.Options{}) {
		opts.Connections = DefaultConnections
	}
	hub, err := wshub.New(opts.Connections)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	s := &Server{
		app:          a,
		feed:         feed,
		authenticate: opts.Authenticate,
		welcome:      opts.Welcome,
		admit:        opts.Admit,
		hosts:        append(slices.Clone(localHosts), opts.AllowedHosts...),
		origins:      append([]string{"localhost:*", "127.0.0.1:*", "[::1]:*"}, opts.AllowedOrigins...),
		hub:          hub,
	}
	attachments := cors.New(cors.Options{
		AllowOriginFunc: s.allowsOrigin,
		AllowedMethods:  []string{http.MethodGet},
		AllowedHeaders:  []string{"Authorization"},
		MaxAge:          600,
	}).Handler(http.HandlerFunc(s.attachment))
	uploads := cors.New(cors.Options{
		AllowOriginFunc: s.allowsOrigin,
		AllowedMethods:  []string{http.MethodPost},
		AllowedHeaders:  []string{"Authorization", "Content-Type", "Content-Disposition"},
		MaxAge:          600,
	}).Handler(http.HandlerFunc(s.upload))
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+auth.HealthPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("GET "+AttachmentPath, attachments)
	mux.Handle("OPTIONS "+AttachmentPath, attachments)
	mux.Handle("POST "+UploadPath, uploads)
	mux.Handle("OPTIONS "+UploadPath, uploads)
	mux.HandleFunc("GET /", s.connect)
	s.handler = mux
	return s, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s,
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

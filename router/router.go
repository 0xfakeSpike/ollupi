// Package router finds the shard each client connection belongs on, ahead of the shards: through a load balancer's
// callout (router/extproc), or as a proxy (Router) where the load balancer cannot route on its own. The shards
// themselves only check that a connection is theirs, through gateway.Options.Placements.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/0xfakeSpike/ollupi/gateway"
	lru "github.com/hashicorp/golang-lru/v2"
)

// Directory finds the shard of each user, placing those new to it, and where each shard runs; Catalog is one.
type Directory interface {
	Assign(ctx context.Context, user string) (string, error)
	Address(ctx context.Context, shard string) (string, error)
}

// Locator says which shard the user a connection's token belongs to is on. The Router uses it, and so does a load
// balancer's callout (router/extproc) where the load balancer holds the connections. It remembers where each user it
// found is, since a user stays on their shard once placed: the reconnections that follow an outage reach the
// directory only for users it has not seen.
type Locator struct {
	authenticate func(token string) (string, bool)
	directory    Directory
	placed       *lru.Cache[string, string]
}

// rememberedUsers bounds what a Locator remembers, to some tens of MB.
const rememberedUsers = 1 << 18

// ErrUnknownToken says a connection carried no token, or one that is no one's.
var ErrUnknownToken = errors.New("router: invalid or missing token")

func NewLocator(authenticate func(token string) (string, bool), directory Directory) (*Locator, error) {
	if authenticate == nil || directory == nil {
		return nil, errors.New("router: a locator needs to authenticate users and find their shards")
	}
	placed, err := lru.New[string, string](rememberedUsers)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	return &Locator{authenticate: authenticate, directory: directory, placed: placed}, nil
}

// Locate says whose token the headers of a connection carry and which shard they are on.
func (l *Locator) Locate(ctx context.Context, header http.Header) (user, shard string, err error) {
	user, ok := l.authenticate(gateway.TokenOf(header))
	if !ok {
		return "", "", ErrUnknownToken
	}
	shard, err = l.shardOf(ctx, user)
	return user, shard, err
}

func (l *Locator) shardOf(ctx context.Context, user string) (string, error) {
	if shard, ok := l.placed.Get(user); ok {
		return shard, nil
	}
	shard, err := l.directory.Assign(ctx, user)
	if err != nil {
		return "", err
	}
	l.placed.Add(user, shard)
	return shard, nil
}

// Router sends each connection to the shard of its user, holding it on the way. Where a load balancer can place
// connections itself through router/extproc, the router is where it sends those its callout could not place. It
// keeps nothing, so any number of them can run. A connection that drops when a shard changes hands comes back
// through it to the new holder.
type Router struct {
	locator   *Locator
	transport *http.Transport
}

// shardAnswerTimeout is how long a shard may take to answer a connection it took: a shard that hangs would otherwise
// hold the client forever. The WebSocket that follows is not bound by it.
const shardAnswerTimeout = 30 * time.Second

func NewRouter(locator *Locator) *Router {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = shardAnswerTimeout
	return &Router{locator: locator, transport: transport}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == gateway.HealthPath {
		w.WriteHeader(http.StatusOK)
		return
	}
	user, shard, err := rt.locator.Locate(r.Context(), r.Header)
	if errors.Is(err, ErrUnknownToken) {
		http.Error(w, "invalid or missing gateway token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.Error("user not placed on a shard", "user", user, "err", err)
		http.Error(w, "no shard can take this user now", http.StatusServiceUnavailable)
		return
	}
	address, err := rt.locator.directory.Address(r.Context(), shard)
	if err != nil {
		slog.Error("shard not found", "shard", shard, "err", err)
		http.Error(w, "the user's shard cannot be found now", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(address)
	if err != nil {
		slog.Error("shard address not usable", "shard", shard, "address", address, "err", err)
		http.Error(w, "the user's shard cannot be reached now", http.StatusServiceUnavailable)
		return
	}
	proxy := httputil.ReverseProxy{
		Transport: rt.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		// The shard may be changing hands: the client tries again, and by then the new holder has published itself.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("shard not reached", "shard", shard, "address", address, "err", err)
			http.Error(w, "the user's shard cannot be reached now; try again", http.StatusServiceUnavailable)
		},
	}
	proxy.ServeHTTP(w, r)
}

var (
	_ Directory            = (*Catalog)(nil)
	_ gateway.Placements   = (*Catalog)(nil)
	_ gateway.LoadReporter = (*Catalog)(nil)
)

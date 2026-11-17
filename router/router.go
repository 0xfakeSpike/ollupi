// Package router finds the shard each client connection belongs on, ahead of the shards: through a load balancer's
// callout (router/extproc), or as a proxy (Router) where the load balancer cannot route on its own. The shards
// themselves only check that a connection is theirs, through shard.Member.Admit.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/0xfakeSpike/ollupi/catalog"
	"github.com/0xfakeSpike/ollupi/gateway"
)

type Locator struct {
	authenticate gateway.Authenticate
	catalog      catalog.Catalog
}

// lookupTimeout bounds how long placing one connection may wait on the token's check and on the catalog, which
// would otherwise hold the client for as long as either hangs.
const lookupTimeout = 10 * time.Second

func NewLocator(authenticate gateway.Authenticate, c catalog.Catalog) (*Locator, error) {
	if authenticate == nil || c == nil {
		return nil, errors.New("router: a locator needs to authenticate users and find their shards")
	}
	return &Locator{authenticate: authenticate, catalog: c}, nil
}

// Locate returns gateway.ErrUnknownToken when the token is no one's.
func (l *Locator) Locate(ctx context.Context, header http.Header) (shard, address string, err error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	user, err := l.authenticate(ctx, gateway.TokenOf(header))
	if errors.Is(err, gateway.ErrUnknownToken) {
		return "", "", err
	}
	if err != nil {
		return "", "", fmt.Errorf("router: the token could not be checked: %w", err)
	}
	return l.catalog.Locate(ctx, user)
}

// Router keeps nothing, so any number of them can run. Where a load balancer places connections through
// router/extproc, the router gets those its callout could not place.
type Router struct {
	locator   *Locator
	transport *http.Transport
}

// A shard that hangs would otherwise hold the client forever. The WebSocket that follows is not bound by it.
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
	_, address, err := rt.locator.Locate(r.Context(), r.Header)
	if errors.Is(err, gateway.ErrUnknownToken) {
		http.Error(w, "invalid or missing gateway token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "no shard can take this user now", http.StatusServiceUnavailable)
		return
	}
	if address == "" {
		http.Error(w, "no process holds the user's shard now; try again", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(address)
	if err != nil {
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
			http.Error(w, "the user's shard cannot be reached now; try again", http.StatusServiceUnavailable)
		},
	}
	proxy.ServeHTTP(w, r)
}

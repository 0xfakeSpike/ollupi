package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	lru "github.com/hashicorp/golang-lru/v2"
)

// Directory finds a user's shard and where it runs; repo.Catalog is one.
type Directory interface {
	ShardOf(user string) (string, error)
	Address(shard string) (string, error)
}

// Locator says which shard the user a connection's token belongs to is on. The router uses it, and so does a load
// balancer's callout (gateway/extproc) where the load balancer holds the connections. It remembers where each user it
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
var ErrUnknownToken = errors.New("gateway: invalid or missing token")

func NewLocator(authenticate func(token string) (string, bool), directory Directory) (*Locator, error) {
	if authenticate == nil || directory == nil {
		return nil, errors.New("gateway: a locator needs to authenticate users and find their shards")
	}
	placed, err := lru.New[string, string](rememberedUsers)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}
	return &Locator{authenticate: authenticate, directory: directory, placed: placed}, nil
}

// Locate says whose token the headers of a connection carry and which shard they are on.
func (l *Locator) Locate(header http.Header) (user, shard string, err error) {
	user, ok := l.authenticate(tokenOf(header))
	if !ok {
		return "", "", ErrUnknownToken
	}
	shard, err = l.shardOf(user)
	return user, shard, err
}

func (l *Locator) shardOf(user string) (string, error) {
	if shard, ok := l.placed.Get(user); ok {
		return shard, nil
	}
	shard, err := l.directory.ShardOf(user)
	if err != nil {
		return "", err
	}
	l.placed.Add(user, shard)
	return shard, nil
}

// Router sends each connection to the shard of its user, holding it on the way. Where a load balancer can place
// connections itself through gateway/extproc, the router is where it sends those its callout could not place. It
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
	if r.URL.Path == healthPath {
		w.WriteHeader(http.StatusOK)
		return
	}
	user, shard, err := rt.locator.Locate(r.Header)
	if errors.Is(err, ErrUnknownToken) {
		http.Error(w, "invalid or missing gateway token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.Error("user not placed on a shard", "user", user, "err", err)
		http.Error(w, "no shard can take this user now", http.StatusServiceUnavailable)
		return
	}
	address, err := rt.locator.directory.Address(shard)
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

// OnShard narrows a shard's Authenticate to the users placed on it, so that a connection that reaches the wrong shard
// cannot start a second home for its user.
func OnShard(shard string, locator *Locator) func(string) (string, bool) {
	return func(token string) (string, bool) {
		user, ok := locator.authenticate(token)
		if !ok {
			return "", false
		}
		placed, err := locator.shardOf(user)
		if err != nil {
			slog.Error("user not placed on a shard", "user", user, "err", err)
			return "", false
		}
		if placed != shard {
			slog.Warn("connection refused on the wrong shard", "user", user, "shard", shard, "theirs", placed)
			return "", false
		}
		return user, true
	}
}

// healthPath answers 200 while the process serves, for a load balancer's health check: a shard serves only once it
// holds its lease, so the check sends a shard's connections to the process that holds it.
const healthPath = "/healthz"

// tokenProtocol is the WebSocket subprotocol a browser, which cannot set Authorization, offers just before its token.
const tokenProtocol = "ollupi.token"

// tokenOf reads the token a client sent. It stays out of the URL, which access logs keep.
func tokenOf(header http.Header) string {
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

var _ Directory = (*repo.Catalog)(nil)

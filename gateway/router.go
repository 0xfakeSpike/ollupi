package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/repo"
)

// Directory finds a user's shard and where it runs; repo.Catalog is one.
type Directory interface {
	ShardOf(user string) (string, error)
	Address(shard string) (string, error)
}

// Router is the one address clients reach: it sends each connection to the shard of its user. It keeps nothing, so any
// number of them can run. A connection that drops when a shard changes hands comes back through it to the new holder.
type Router struct {
	authenticate func(token string) (string, bool)
	directory    Directory
}

func NewRouter(authenticate func(token string) (string, bool), directory Directory) (*Router, error) {
	if authenticate == nil || directory == nil {
		return nil, errors.New("gateway: a router needs to authenticate users and find their shards")
	}
	return &Router{authenticate: authenticate, directory: directory}, nil
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := rt.authenticate(tokenOf(r))
	if !ok {
		http.Error(w, "invalid or missing gateway token", http.StatusUnauthorized)
		return
	}
	shard, err := rt.directory.ShardOf(user)
	if err != nil {
		slog.Error("user not placed on a shard", "user", user, "err", err)
		http.Error(w, "no shard can take this user now", http.StatusServiceUnavailable)
		return
	}
	address, err := rt.directory.Address(shard)
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

// OnShard narrows Authenticate to the users placed on one shard, so that a connection that reaches the wrong shard
// cannot start a second home for its user.
func OnShard(shard string, directory Directory, authenticate func(token string) (string, bool)) func(string) (string, bool) {
	return func(token string) (string, bool) {
		user, ok := authenticate(token)
		if !ok {
			return "", false
		}
		placed, err := directory.ShardOf(user)
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

// tokenProtocol is the WebSocket subprotocol a browser, which cannot set Authorization, offers just before its token.
const tokenProtocol = "ollupi.token"

// tokenOf reads the token a client sent. It stays out of the URL, which access logs keep.
func tokenOf(r *http.Request) string {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return bearer
	}
	var offered []string
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
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

package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
)

// Authenticate says whose token this is: ErrUnknownToken for one that is no one's, or for none; any other error says
// it could not be told now, as when the service that gave the tokens out does not answer, and the client tries again.
type Authenticate func(ctx context.Context, token string) (user string, err error)

var ErrUnknownToken = errors.New("invalid or missing token")

// tokenProtocol is the WebSocket subprotocol a browser, which cannot set Authorization, offers just before its token.
const tokenProtocol = "ollupi.token"

// The token stays out of the URL, which access logs keep.
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

func (s *Server) authorize(r *http.Request) (string, int, string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(s.hosts, strings.Trim(host, "[]")) {
		return "", http.StatusForbidden, "host not allowed"
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	user, err := s.authenticate(ctx, TokenOf(r.Header))
	switch {
	case errors.Is(err, ErrUnknownToken):
		return "", http.StatusUnauthorized, "invalid or missing gateway token"
	case err != nil:
		return "", http.StatusServiceUnavailable, "the token cannot be checked now; try again"
	}
	return user, 0, ""
}

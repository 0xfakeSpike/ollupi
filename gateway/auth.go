package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
)

type Authenticate func(ctx context.Context, token string) (user string, err error)

var ErrUnknownToken = errors.New("invalid or missing token")

const tokenProtocol = "ollupi.token"

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

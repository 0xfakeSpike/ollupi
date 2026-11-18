package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/auth"
)

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
	user, err := s.authenticate(ctx, auth.TokenOf(r.Header))
	switch {
	case errors.Is(err, auth.ErrUnknownToken):
		return "", http.StatusUnauthorized, "invalid or missing gateway token"
	case err != nil:
		return "", http.StatusServiceUnavailable, "the token cannot be checked now; try again"
	}
	return user, 0, ""
}

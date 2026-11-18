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

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !slices.Contains(s.hosts, strings.Trim(host, "[]")) {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	user, err := s.authenticate(ctx, auth.TokenOf(r.Header))
	switch {
	case errors.Is(err, auth.ErrUnknownToken):
		http.Error(w, "invalid or missing gateway token", http.StatusUnauthorized)
		return "", false
	case err != nil:
		s.hub.Refuse(w, "the token cannot be checked now; try again")
		return "", false
	}
	return user, true
}

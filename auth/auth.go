package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// Identity is who a connection acts as: a user, and whether it is also the
// deployment's operator, who may change what is no user's own, as the
// catalog of models.
type Identity struct {
	User     string
	Operator bool
}

type Authenticate func(ctx context.Context, token string) (Identity, error)

var ErrUnknownToken = errors.New("invalid or missing token")

const TokenProtocol = "ollupi.token"

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
	if i := slices.Index(offered, TokenProtocol); i >= 0 && i+1 < len(offered) {
		return offered[i+1]
	}
	return ""
}

const HealthPath = "/healthz"

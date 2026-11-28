package auth

import (
	"context"
	"crypto/subtle"
	"regexp"
	"slices"
	"strings"
)

// ProfileID is what a profile's id may be: it names folders on the server's
// disk and the part of a token after its dot.
var ProfileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Identify names the identity a token's secret acts as, without checking
// that the profile is one the store holds: "<secret>.<profile>". A token
// with the operator's secret acts as the profile and as the deployment's
// operator.
func Identify(token, secret, operator string) (Identity, error) {
	given, profile, ok := strings.Cut(token, ".")
	if !ok || !ProfileID.MatchString(profile) {
		return Identity{}, ErrUnknownToken
	}
	switch {
	case operator != "" && subtle.ConstantTimeCompare([]byte(given), []byte(operator)) == 1:
		return Identity{User: profile, Operator: true}, nil
	case secret != "" && subtle.ConstantTimeCompare([]byte(given), []byte(secret)) == 1:
		return Identity{User: profile}, nil
	}
	return Identity{}, ErrUnknownToken
}

// Profiles authenticates a connection by a token of the server's secret and
// a profile, joined by a dot: "<secret>.<profile>". The profile must be one
// held names, so a token names a user the store has, never one it can make
// up.
func Profiles(secret, operator string, held func(ctx context.Context) []string) Authenticate {
	return func(ctx context.Context, token string) (Identity, error) {
		id, err := Identify(token, secret, operator)
		if err != nil {
			return Identity{}, err
		}
		if !slices.Contains(held(ctx), id.User) {
			return Identity{}, ErrUnknownToken
		}
		return id, nil
	}
}

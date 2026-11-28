package auth

import (
	"context"
	"errors"
	"testing"
)

func TestIdentify(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		user     string
		operator bool
		ok       bool
	}{
		{name: "a user's token", token: "secret.default", user: "default", ok: true},
		{name: "the operator's token", token: "operator.work", user: "work", operator: true, ok: true},
		{name: "another secret", token: "other.default"},
		{name: "no profile", token: "secret"},
		{name: "an empty secret", token: ".default"},
		{name: "a profile that is not an id", token: "secret.a/b"},
		{name: "a dot in the profile", token: "secret.a.b"},
		{name: "nothing", token: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := Identify(c.token, "secret", "operator")
			if !c.ok {
				if !errors.Is(err, ErrUnknownToken) {
					t.Fatalf("got %v, %v; want ErrUnknownToken", id, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id.User != c.user || id.Operator != c.operator {
				t.Fatalf("got %+v; want user %q, operator %v", id, c.user, c.operator)
			}
		})
	}
}

func TestProfiles(t *testing.T) {
	held := func(context.Context) []string { return []string{"default"} }
	authenticate := Profiles("secret", "operator", held)
	if id, err := authenticate(context.Background(), "secret.default"); err != nil || id.User != "default" || id.Operator {
		t.Fatalf("got %+v, %v", id, err)
	}
	// Valid secrets, but a profile the store does not hold are refused.
	for _, token := range []string{"secret.work", "operator.work"} {
		if _, err := authenticate(context.Background(), token); !errors.Is(err, ErrUnknownToken) {
			t.Fatalf("%s: got %v", token, err)
		}
	}
}

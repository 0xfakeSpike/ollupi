package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/auth"
)

func TestServeNeedsBothTokens(t *testing.T) {
	t.Setenv("OLLUPI_GATEWAY_TOKEN", "")
	t.Setenv("OLLUPI_OPERATOR_TOKEN", "")
	if code := serve([]string{"--home", t.TempDir()}); code != 2 {
		t.Fatalf("got %d; want 2", code)
	}
}

func TestServeRefusesOneSecretForBoth(t *testing.T) {
	t.Setenv("OLLUPI_GATEWAY_TOKEN", "same")
	t.Setenv("OLLUPI_OPERATOR_TOKEN", "same")
	if code := serve([]string{"--home", t.TempDir()}); code != 2 {
		t.Fatalf("got %d; want 2", code)
	}
}

func TestANewStoreStartsWithTheAdmin(t *testing.T) {
	ctx := context.Background()
	h, err := newServer(ctx, t.TempDir(), "gateway", "operator", nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := h.Profiles(ctx); !slices.Equal(got, []string{AdminProfile}) {
		t.Fatalf("the store holds %v", got)
	}
	u, err := h.App.User(ctx, AdminProfile)
	if err != nil || u.Name != AdminName {
		t.Fatalf("the admin is %+v, %v", u, err)
	}

	// The operator's token connects as it, with the operator's rights.
	id, err := h.Authenticate(ctx, "operator."+AdminProfile)
	if err != nil || id.User != AdminProfile || !id.Operator {
		t.Fatalf("got %+v, %v", id, err)
	}
	// The gateway's token connects as it too, without them.
	id, err = h.Authenticate(ctx, "gateway."+AdminProfile)
	if err != nil || id.User != AdminProfile || id.Operator {
		t.Fatalf("got %+v, %v", id, err)
	}

	// Connecting does not make other profiles.
	if _, err := h.Authenticate(ctx, "operator.other"); !errors.Is(err, auth.ErrUnknownToken) {
		t.Fatalf("a profile was made by connecting: %v", err)
	}
}

func TestASecondStartKeepsTheStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	h, err := newServer(ctx, home, "gateway", "operator", nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.CreateUser(ctx, "Work"); err != nil {
		t.Fatal(err)
	}
	h.Close()

	h, err = newServer(ctx, home, "gateway", "operator", nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := h.Profiles(ctx); len(got) != 2 {
		t.Fatalf("the store holds %v", got)
	}
}

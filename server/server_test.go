package server

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/skill"
)

func TestThisMachineHidesTheStoreAndTheTokens(t *testing.T) {
	home := t.TempDir()
	m := thisMachine(home, home, "default", "", noFirst)
	if !slices.Contains(m.Config.DenyRead, filepath.Join(home, "data")) {
		t.Errorf("the store is not hidden from commands: %v", m.Config.DenyRead)
	}
	if !slices.Contains(m.Config.HideEnv, "OLLUPI_") {
		t.Errorf("the tokens are not hidden from commands: %v", m.Config.HideEnv)
	}
}

func TestEachEnvironmentIsAFolderOfItsOwn(t *testing.T) {
	home := t.TempDir()
	m := thisMachine(home, home, "default", "", noFirst)
	a, err := m.Open(context.Background(), sandbox.Env{User: "a", ID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Open(context.Background(), sandbox.Env{User: "b", ID: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enforced() || a.Roots()[0] == b.Roots()[0] || !strings.HasSuffix(a.Roots()[0], filepath.Join("users", "a", "e1")) {
		t.Errorf("environments in %v and %v", a.Roots(), b.Roots())
	}
}

func noFirst(context.Context, string) (string, error) { return "", nil }

func TestTheFirstProfilesFirstEnvironmentIsTheScratchFolder(t *testing.T) {
	home := t.TempDir()
	m := thisMachine(home, home, "admin", "scratch", func(_ context.Context, user string) (string, error) { return "first-of-" + user, nil })
	open := func(user, env string) string {
		t.Helper()
		box, err := m.Open(context.Background(), sandbox.Env{User: user, ID: env})
		if err != nil {
			t.Fatal(err)
		}
		return box.Roots()[0]
	}
	if got := open("admin", "first-of-admin"); !strings.HasSuffix(got, "/scratch") {
		t.Errorf("the first profile's first environment is %s", got)
	}
	if got := open("admin", "another"); !strings.HasSuffix(got, filepath.Join("users", "admin", "another")) {
		t.Errorf("another environment of the first profile is %s", got)
	}
	if got := open("work", "first-of-work"); !strings.HasSuffix(got, filepath.Join("users", "work", "first-of-work")) {
		t.Errorf("another profile's first environment is %s", got)
	}
}

func TestANewStoreGetsItsFirstProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	starter := func() ([]skill.Package, error) {
		return []skill.Package{{Name: "starter", Description: "d", Instructions: "i", Roles: []string{string(agent.FullMain)}}}, nil
	}
	h, err := New(ctx, Config{
		Home: t.TempDir(), Token: "secret", OperatorToken: "boss",
		First: Profile{ID: "admin", Name: "Admin"}, StarterSkills: starter, Scratch: "scratch",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := h.Profiles(ctx); !slices.Equal(got, []string{"admin"}) {
		t.Fatalf("the store holds %v", got)
	}
	u, err := h.App.User(ctx, "admin")
	if err != nil || u.Name != "Admin" {
		t.Fatalf("the first profile is %+v, %v", u, err)
	}
	list, _, err := h.App.Skills(ctx, "admin")
	if err != nil || !slices.ContainsFunc(list, func(s skill.Skill) bool { return s.Name == "starter" }) {
		t.Fatalf("skills = %+v, %v", list, err)
	}

	// The operator's token acts as it, with the operator's rights; the
	// gateway's as a user. Connecting makes no other profile.
	id, err := h.Authenticate(ctx, "boss.admin")
	if err != nil || id.User != "admin" || !id.Operator {
		t.Fatalf("got %+v, %v", id, err)
	}
	id, err = h.Authenticate(ctx, "secret.admin")
	if err != nil || id.User != "admin" || id.Operator {
		t.Fatalf("got %+v, %v", id, err)
	}
	if _, err := h.Authenticate(ctx, "secret.other"); !errors.Is(err, auth.ErrUnknownToken) {
		t.Fatalf("a profile was made by connecting: %v", err)
	}
}

func TestABuildWithAProfileLeavesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	home := t.TempDir()
	h, err := New(ctx, Config{Home: home, Token: "secret", OperatorToken: "boss", First: Profile{ID: "admin", Name: "Admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.CreateUser(ctx, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = New(ctx, Config{Home: home, Token: "secret", OperatorToken: "boss", First: Profile{ID: "admin", Name: "Admin"}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if got := h.Profiles(ctx); len(got) != 2 {
		t.Fatalf("the store holds %v", got)
	}
}

func TestAServerCanOfferAnotherSandbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := New(ctx, Config{
		Home: t.TempDir(), Token: "secret", OperatorToken: "boss", First: Profile{ID: "admin"},
		// A cloud's sandbox would plug in here; a folder is enough to prove
		// the environment carries the provider it was made with.
		Sandboxes: map[string]sandbox.Provider{"cloud": sandbox.Local{Dir: t.TempDir()}},
		Sandbox:   "cloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	envs, _, err := h.App.Environments(ctx, "admin")
	if err != nil || len(envs) != 1 || envs[0].Provider != "cloud" {
		t.Fatalf("environments = %+v, %v", envs, err)
	}
}

func TestAServerRefusesAFirstSandboxItDoesNotOffer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := New(ctx, Config{Home: t.TempDir(), Token: "secret", OperatorToken: "boss", First: Profile{ID: "admin"}, Sandbox: "cloud"}); err == nil {
		t.Fatal("a server started with a sandbox it does not offer")
	}
}

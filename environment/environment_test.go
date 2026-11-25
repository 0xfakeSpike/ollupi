package environment

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
)

// cloud is a provider whose sandboxes are made on the first open and found
// by the id kept after.
type cloud struct {
	mu      sync.Mutex
	made    int
	resumed []string
	removed []string
	closed  int
}

func (*cloud) Name() string { return "A cloud sandbox" }

func (*cloud) Check(config json.RawMessage) error {
	if string(config) == `{"template":"nope"}` {
		return errors.New("no such template")
	}
	return nil
}

func (c *cloud) Open(_ context.Context, env sandbox.Env) (sandbox.Box, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if env.State == nil {
		c.made++
		if err := env.Keep(json.RawMessage(`{"sandbox":"sb-` + env.ID + `"}`)); err != nil {
			return nil, err
		}
	} else {
		c.resumed = append(c.resumed, string(env.State))
	}
	return &box{Box: sandbox.Remote(nil, []string{"/work"}, "cloud"), c: c}, nil
}

func (c *cloud) Remove(_ context.Context, env sandbox.Env) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = append(c.removed, string(env.State))
	return nil
}

type box struct {
	sandbox.Box
	c *cloud
}

func (b *box) Close() error {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	b.c.closed++
	return nil
}

func setup(t *testing.T, home string, c *cloud) (*Boxes, store.Store) {
	t.Helper()
	st, err := sqlite.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := New(st, map[string]sandbox.Provider{
		"local": sandbox.Local{Dir: t.TempDir(), Config: sandbox.Config{Mode: string(sandbox.ModeFullAccess)}},
		"cloud": c,
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b, st
}

func TestAnEnvironmentIsOpenedOnceAndFoundAgainAfterARestart(t *testing.T) {
	ctx, home, c := context.Background(), t.TempDir(), &cloud{}
	b, st := setup(t, home, c)
	if got := b.Providers(); !slices.Equal(got, []Provider{{ID: "local", Name: sandbox.LocalName}, {ID: "cloud", Name: "A cloud sandbox"}}) {
		t.Errorf("providers %+v", got)
	}
	first := b.First()
	if first.Provider != "local" || first.Name != sandbox.LocalName {
		t.Errorf("a user's first environment %+v", first)
	}
	if b.Check("cloud", json.RawMessage(`{"template":"nope"}`)) == nil || !errors.Is(b.Check("elsewhere", nil), ErrUnknownProvider) {
		t.Error("a sandbox no provider can make was taken")
	}
	there := account.NewEnvironment("Cloud", "cloud", json.RawMessage(`{"template":"base"}`))
	there.Created = time.Now().UTC().Add(time.Second)
	err := st.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("u", first); err != nil {
			return err
		}
		return tx.PutEnvironment("u", there)
	})
	if err != nil {
		t.Fatal(err)
	}

	mine, err := b.In(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Enforced() || len(mine.Roots()) != 1 {
		t.Errorf("the first environment's box: %s", mine.Summary())
	}
	var boxes []sandbox.Box
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range 8 {
		wg.Go(func() {
			got, err := b.In(ctx, "u", there.ID)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			boxes = append(boxes, got)
			mu.Unlock()
		})
	}
	wg.Wait()
	if c.made != 1 || slices.ContainsFunc(boxes, func(x sandbox.Box) bool { return x != boxes[0] }) {
		t.Errorf("made %d sandboxes for one environment", c.made)
	}
	kept, err := readEnv(st, "u", there.ID)
	if err != nil || string(kept.State) != `{"sandbox":"sb-`+there.ID+`"}` {
		t.Errorf("the state kept %s, %v", kept.State, err)
	}

	// After a restart the sandbox is resumed, not made again.
	b.Close()
	st.Close()
	b2, _ := setup(t, home, c)
	if _, err := b2.In(ctx, "u", there.ID); err != nil {
		t.Fatal(err)
	}
	if c.made != 1 || !slices.Equal(c.resumed, []string{`{"sandbox":"sb-` + there.ID + `"}`}) {
		t.Errorf("after a restart: made %d, resumed %v", c.made, c.resumed)
	}
	closed := c.closed
	if err := b2.Remove(ctx, "u", kept); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.closedAt() == closed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.closedAt() != closed+1 || len(c.removed) != 1 {
		t.Errorf("removing it closed %d boxes and removed %v", c.closedAt()-closed, c.removed)
	}
}

func (c *cloud) closedAt() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func TestAnEnvironmentOfAProviderNotOfferedDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	b, st := setup(t, t.TempDir(), &cloud{})
	gone := account.NewEnvironment("Gone", "retired", nil)
	gone.Created = time.Now().UTC().Add(time.Second)
	err := st.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("u", b.First()); err != nil {
			return err
		}
		return tx.PutEnvironment("u", gone)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.In(ctx, "u", gone.ID); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("opened %v", err)
	}
	if _, err := New(st, map[string]sandbox.Provider{"cloud": &cloud{}}, "local"); err == nil {
		t.Error("a first environment of a provider not offered")
	}
}

func readEnv(st store.Store, user, id string) (e account.Environment, err error) {
	err = st.Read(context.Background(), func(r store.Reader) error { e, err = r.Environment(user, id); return err })
	return e, err
}

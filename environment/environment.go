// Package environment opens the sandboxes users' conversations run in. An
// environment is one sandbox of one of the deployment's providers
// (sandbox.Provider): this machine, or a cloud's. Each is opened the first
// time something needs its box and kept open after, so every conversation
// in it, and every tool, works in the same sandbox; it is closed when the
// environment is deleted or the deployment stops.
package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
)

// ErrUnknownProvider is an environment of a provider the deployment does
// not offer.
var ErrUnknownProvider = errors.New("the deployment offers no such sandbox")

// Provider is one the deployment offers, as a person picks it.
type Provider struct {
	// ID is what an environment names it by.
	ID string
	// Name is what its sandboxes are called.
	Name string
}

// Boxes are the boxes of users' environments.
type Boxes struct {
	st        store.Store
	providers map[string]sandbox.Provider
	first     string

	mu     sync.Mutex
	open   map[string]*opened
	closed bool
	sub    *store.Subscription
}

// opened is one environment's box, or the open that makes it.
type opened struct {
	ready chan struct{}
	box   sandbox.Box
	err   error
}

// New makes the boxes of the deployment's providers, by id. first is the
// provider a user's first environment is a sandbox of: every user has one,
// made with them.
func New(st store.Store, providers map[string]sandbox.Provider, first string) (*Boxes, error) {
	if len(providers) == 0 {
		return nil, errors.New("environment: the deployment offers no sandbox")
	}
	if providers[first] == nil {
		return nil, fmt.Errorf("environment: the first environment is a sandbox of %q, which is not offered", first)
	}
	b := &Boxes{st: st, providers: maps.Clone(providers), first: first, open: map[string]*opened{}}
	b.sub = st.Feed().SubscribeAll(func(e store.Event) {
		if e.Owned != nil && e.Owned.EnvironmentDeleted != "" {
			b.drop(e.Owned.EnvironmentDeleted)
		}
	})
	return b, nil
}

// Providers are the sandboxes the deployment offers, the first's first.
func (b *Boxes) Providers() []Provider {
	ids := slices.Sorted(maps.Keys(b.providers))
	slices.SortStableFunc(ids, func(x, y string) int {
		switch {
		case x == b.first:
			return -1
		case y == b.first:
			return 1
		}
		return 0
	})
	out := make([]Provider, len(ids))
	for i, id := range ids {
		out[i] = Provider{ID: id, Name: b.providers[id].Name()}
	}
	return out
}

// First is the environment a new user is made with.
func (b *Boxes) First() account.Environment {
	return account.NewEnvironment(b.providers[b.first].Name(), b.first, nil)
}

// Check says whether an environment may be made of provider with config.
func (b *Boxes) Check(provider string, config json.RawMessage) error {
	p := b.providers[provider]
	if p == nil {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
	return p.Check(config)
}

// In is the box of one of user's environments; an empty environment is the
// user's first. It is opened the first time and the same box after.
func (b *Boxes) In(ctx context.Context, user, environment string) (sandbox.Box, error) {
	var env account.Environment
	err := b.st.Read(ctx, func(r store.Reader) error {
		if environment != "" {
			var err error
			env, err = r.Environment(user, environment)
			return err
		}
		envs, err := r.Environments(user)
		if err != nil {
			return err
		}
		if len(envs) == 0 {
			return fmt.Errorf("environment: user %s has no environment", user)
		}
		env = envs[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b.box(ctx, user, env)
}

func (b *Boxes) box(ctx context.Context, user string, env account.Environment) (sandbox.Box, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("environment: the deployment is stopping")
	}
	o := b.open[env.ID]
	if o == nil {
		o = &opened{ready: make(chan struct{})}
		b.open[env.ID] = o
		b.mu.Unlock()
		o.box, o.err = b.make(ctx, user, env)
		if o.err != nil {
			b.mu.Lock()
			if b.open[env.ID] == o {
				delete(b.open, env.ID)
			}
			b.mu.Unlock()
		}
		close(o.ready)
	} else {
		b.mu.Unlock()
	}
	select {
	case <-o.ready:
		return o.box, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// make opens an environment's sandbox, outside the lock: a cloud's takes a
// while to start, and no other environment waits for it.
func (b *Boxes) make(ctx context.Context, user string, env account.Environment) (sandbox.Box, error) {
	p := b.providers[env.Provider]
	if p == nil {
		return nil, fmt.Errorf("environment %s: %w: %q", env.Name, ErrUnknownProvider, env.Provider)
	}
	box, err := p.Open(context.WithoutCancel(ctx), b.envOf(user, env))
	if err != nil {
		return nil, fmt.Errorf("environment %s: %w", env.Name, err)
	}
	return box, nil
}

func (b *Boxes) envOf(user string, env account.Environment) sandbox.Env {
	return sandbox.Env{
		User: user, ID: env.ID, Config: none(env.Config), State: none(env.State),
		Keep: func(state json.RawMessage) error {
			return b.st.Update(context.Background(), func(tx store.Tx) error { return tx.KeepEnvironmentState(user, env.ID, state) })
		},
	}
}

// none is a stored JSON value as a provider takes it: nil when nothing was
// stored.
func none(v json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(v); len(t) == 0 || bytes.Equal(t, []byte("{}")) || bytes.Equal(t, []byte("null")) {
		return nil
	}
	return v
}

// Remove gets rid of an environment's sandbox, before the environment is
// deleted: its box is closed and its provider removes what it made.
func (b *Boxes) Remove(ctx context.Context, user string, env account.Environment) error {
	b.drop(env.ID)
	p := b.providers[env.Provider]
	if p == nil {
		return nil
	}
	if err := p.Remove(ctx, b.envOf(user, env)); err != nil {
		return fmt.Errorf("environment %s: %w", env.Name, err)
	}
	return nil
}

// drop closes an environment's box, once its open is done.
func (b *Boxes) drop(id string) {
	b.mu.Lock()
	o := b.open[id]
	delete(b.open, id)
	b.mu.Unlock()
	if o == nil {
		return
	}
	go func() {
		<-o.ready
		if o.box != nil {
			o.box.Close()
		}
	}()
}

// Close closes every box. A box being opened is closed once it is.
func (b *Boxes) Close() error {
	b.sub.Close()
	b.mu.Lock()
	b.closed = true
	open := b.open
	b.open = map[string]*opened{}
	b.mu.Unlock()
	var errs []error
	for _, o := range open {
		<-o.ready
		if o.box != nil {
			errs = append(errs, o.box.Close())
		}
	}
	return errors.Join(errs...)
}

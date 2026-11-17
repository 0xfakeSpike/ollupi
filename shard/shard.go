// Package shard runs a process as one shard of a deployment: it holds the shard's lease in the deployment's catalog,
// fences the shard's database with the lease's epoch, reports the shard's load as it renews the lease, and runs the
// engine only while it holds the shard. The engine itself knows nothing of shards; a process that is the only one
// serves without this package.
package shard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/catalog"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

type Member struct {
	ID string
	// Address is where clients reach this process, as the router proxies to it.
	Address string
	// A shard kept for one customer takes no new users, and one its host finds full stops taking them.
	Accepting bool
	Catalog   catalog.Catalog
	TTL       time.Duration
	// Load is by a measure all shards of the deployment share; lower is lighter. Without it the shard reports none.
	Load func() (int64, error)
}

const catalogTimeout = 10 * time.Second

// Run waits until it holds the shard, then serves until ctx ends or the shard is lost. serve's context ends when the
// lease is lost: every write fails from then on, so the process must stop.
func (m *Member) Run(ctx context.Context, store *db.DB, serve func(context.Context) error) error {
	if m.TTL <= 0 {
		return errors.New("shard: a lease needs a TTL")
	}
	owner := uuid.NewString()
	epoch, until, err := m.acquire(ctx, owner)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogTimeout)
		defer cancel()
		m.Catalog.Release(ctx, m.ID, owner, epoch) // a lease not released lapses
	}()
	if err := store.Fence(epoch); err != nil {
		return fmt.Errorf("shard %s: %w", m.ID, err)
	}
	held, lose := context.WithCancelCause(ctx)
	defer lose(nil)
	kept := make(chan struct{})
	go func() {
		defer close(kept)
		m.keep(held, owner, epoch, until, lose)
	}()
	err = serve(held)
	lose(nil)
	<-kept
	if cause := context.Cause(held); errors.Is(cause, db.ErrShardLost) {
		return fmt.Errorf("shard %s: %w", m.ID, cause)
	}
	return err
}

// The catalog counts a lease from when it kept it, so the holder counts it from before it asked, and gives up a
// margin early besides, for its clock to run apart from the catalog's: it stops before any other process can start.
func (m *Member) deadline(asked time.Time) time.Time { return asked.Add(m.TTL - m.TTL/10) }

func (m *Member) acquire(ctx context.Context, owner string) (int64, time.Time, error) {
	for {
		asked := time.Now()
		attempt, cancel := context.WithTimeout(ctx, catalogTimeout)
		epoch, held, _ := m.Catalog.Acquire(attempt, m.ID, owner, m.Address, m.Accepting, m.TTL) // a catalog that fails may be back by the next try
		cancel()
		if held {
			return epoch, m.deadline(asked), nil
		}
		select {
		case <-ctx.Done():
			return 0, time.Time{}, ctx.Err()
		case <-time.After(m.TTL / 3):
		}
	}
}

// A renewal that fails is tried again while the lease lasts. Another process can take the shard only once the lease
// has run out, so a lease run out is a shard lost.
func (m *Member) keep(ctx context.Context, owner string, epoch int64, until time.Time, lose context.CancelCauseFunc) {
	tick := time.NewTicker(m.TTL / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var load int64
		if m.Load != nil {
			load, _ = m.Load() // a load not measured reports as idle this time
		}
		asked := time.Now()
		attempt, cancel := context.WithTimeout(ctx, catalogTimeout)
		err := m.Catalog.Renew(attempt, m.ID, owner, epoch, m.TTL, load)
		cancel()
		if err == nil {
			until = m.deadline(asked)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if time.Now().Before(until) {
			continue
		}
		lose(db.ErrShardLost)
		return
	}
}

// Admit refuses a user this shard does not serve now: the router placed them by what it knew, and a client that comes
// back reaches the right shard once that is put right.
func (m *Member) Admit(ctx context.Context, user string) error {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	shard, err := m.Catalog.ShardOf(ctx, user)
	if err != nil {
		return fmt.Errorf("shard: where user %s is: %w", user, err)
	}
	if shard != m.ID {
		return fmt.Errorf("shard: user %s is on shard %q, not %q", user, shard, m.ID)
	}
	return nil
}

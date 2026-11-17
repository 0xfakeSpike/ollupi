// Package shard runs a process as one shard of a deployment: it holds the shard's lease in the deployment's directory,
// fences the shard's database with the lease's epoch, reports the shard's load as it renews the lease, and runs the
// engine only while it holds the shard. The engine itself knows nothing of shards; a process that is the only one
// serves without this package.
package shard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

// repo/catalog is one.
type Directory interface {
	// Acquire takes the shard for owner when no process holds it, and publishes owner's address; each taking has an
	// epoch above every one before it.
	Acquire(ctx context.Context, shard, owner, address string, accepting bool, ttl time.Duration) (epoch int64, held bool, err error)
	Renew(ctx context.Context, shard, owner string, epoch int64, ttl time.Duration, load int64) error
	Release(ctx context.Context, shard, owner string, epoch int64) error
	ShardOf(ctx context.Context, user string) (string, error)
}

type Member struct {
	ID string
	// Address is where clients reach this process, as the router proxies to it.
	Address string
	// A shard kept for one customer takes no new users, and one its host finds full stops taking them.
	Accepting bool
	Directory Directory
	TTL       time.Duration
	// Load is by a measure all shards of the deployment share; lower is lighter. Without it the shard reports none.
	Load func() (int64, error)
}

const directoryTimeout = 10 * time.Second

// Run waits until it holds the shard, then serves until ctx ends or the shard is lost. serve's context ends when the
// lease is lost: every write fails from then on, so the process must stop.
func (m *Member) Run(ctx context.Context, store *db.DB, serve func(context.Context) error) error {
	if m.TTL <= 0 {
		return errors.New("shard: a lease needs a TTL")
	}
	owner := uuid.NewString()
	epoch, err := m.acquire(ctx, owner)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directoryTimeout)
		defer cancel()
		m.Directory.Release(ctx, m.ID, owner, epoch) // a lease not released lapses
	}()
	if err := store.Fence(epoch); err != nil {
		return fmt.Errorf("shard %s: %w", m.ID, err)
	}
	held, lose := context.WithCancelCause(ctx)
	defer lose(nil)
	kept := make(chan struct{})
	go func() {
		defer close(kept)
		m.keep(held, owner, epoch, lose)
	}()
	err = serve(held)
	lose(nil)
	<-kept
	if cause := context.Cause(held); errors.Is(cause, db.ErrShardLost) {
		return fmt.Errorf("shard %s: %w", m.ID, cause)
	}
	return err
}

func (m *Member) acquire(ctx context.Context, owner string) (int64, error) {
	for {
		attempt, cancel := context.WithTimeout(ctx, directoryTimeout)
		epoch, held, _ := m.Directory.Acquire(attempt, m.ID, owner, m.Address, m.Accepting, m.TTL) // a directory that fails may be back by the next try
		cancel()
		if held {
			return epoch, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(m.TTL / 3):
		}
	}
}

// A renewal that fails is tried again while the lease lasts. Another process can take the shard only once the lease
// has run out, so a lease run out is a shard lost.
func (m *Member) keep(ctx context.Context, owner string, epoch int64, lose context.CancelCauseFunc) {
	tick := time.NewTicker(m.TTL / 3)
	defer tick.Stop()
	until := time.Now().Add(m.TTL)
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
		attempt, cancel := context.WithTimeout(ctx, directoryTimeout)
		err := m.Directory.Renew(attempt, m.ID, owner, epoch, m.TTL, load)
		cancel()
		now := time.Now()
		if err == nil {
			until = now.Add(m.TTL)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if now.Before(until) {
			continue
		}
		lose(db.ErrShardLost)
		return
	}
}

// Admit refuses a user this shard does not serve now: the router placed them by what it knew, and a client that comes
// back reaches the right shard once that is put right.
func (m *Member) Admit(ctx context.Context, user string) error {
	ctx, cancel := context.WithTimeout(ctx, directoryTimeout)
	defer cancel()
	shard, err := m.Directory.ShardOf(ctx, user)
	if err != nil {
		return fmt.Errorf("shard: where user %s is: %w", user, err)
	}
	if shard != m.ID {
		return fmt.Errorf("shard: user %s is on shard %q, not %q", user, shard, m.ID)
	}
	return nil
}

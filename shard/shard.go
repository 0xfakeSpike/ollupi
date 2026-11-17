package shard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/catalog"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

type Member struct {
	ID        string
	Address   string
	Accepting bool
	Catalog   catalog.Catalog
	TTL       time.Duration
	Load      func() (int64, error)
}

const catalogTimeout = 10 * time.Second

func (m *Member) Run(ctx context.Context, st store.Store, serve func(context.Context) error) error {
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
		m.Catalog.Release(ctx, m.ID, owner, epoch)
	}()
	if err := st.Fence(epoch); err != nil {
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
	if cause := context.Cause(held); errors.Is(cause, store.ErrShardLost) {
		return fmt.Errorf("shard %s: %w", m.ID, cause)
	}
	return err
}

func (m *Member) deadline(asked time.Time) time.Time { return asked.Add(m.TTL - m.TTL/10) }

func (m *Member) acquire(ctx context.Context, owner string) (int64, time.Time, error) {
	for {
		asked := time.Now()
		attempt, cancel := context.WithTimeout(ctx, catalogTimeout)
		epoch, held, _ := m.Catalog.Acquire(attempt, m.ID, owner, m.Address, m.Accepting, m.TTL)
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
			load, _ = m.Load()
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
		lose(store.ErrShardLost)
		return
	}
}

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

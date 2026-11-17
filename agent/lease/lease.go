// Package lease claims a shard for this process, so that no other process runs it while this one does.
package lease

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

type Lease struct {
	store *db.DB
	owner string
	stop  context.CancelFunc
	done  sync.WaitGroup
}

// Claim waits until the shard is free, takes it and keeps it. The context it returns ends, and onLost is called, once
// another process holds the shard: every write on store fails from then on, so this process must stop.
func Claim(ctx context.Context, store *db.DB, ttl time.Duration, onLost func()) (*Lease, context.Context, error) {
	owner := uuid.NewString()
	every := ttl / 3
	for {
		held, err := store.ClaimShard(owner, time.Now(), ttl)
		if err != nil {
			return nil, nil, fmt.Errorf("lease: claim the shard: %w", err)
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(every):
		}
	}
	store.Fence(owner)
	held, stop := context.WithCancel(ctx)
	l := &Lease{store: store, owner: owner, stop: stop}
	l.done.Add(1)
	go func() {
		defer l.done.Done()
		defer stop()
		l.keep(held, ttl, every, onLost)
	}()
	return l, held, nil
}

// Storage that fails is retried while the claim runs; once it has run out, or another process holds the shard, this
// process must not go on.
func (l *Lease) keep(ctx context.Context, ttl, every time.Duration, onLost func()) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	until := time.Now().Add(ttl)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		held, err := l.store.ClaimShard(l.owner, now, ttl)
		if held {
			until = now.Add(ttl)
			continue
		}
		if err != nil && now.Before(until) {
			continue
		}
		if onLost != nil {
			onLost()
		}
		return
	}
}

func (l *Lease) Release() {
	l.stop()
	l.done.Wait()
	l.store.ReleaseShard(l.owner) // a claim not released lapses with its lease
}

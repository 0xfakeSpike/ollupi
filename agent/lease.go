package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// claimShard waits until this process holds the shard, and returns the context that ends when it no longer does.
func (a *Agent) claimShard(ctx context.Context) (context.Context, error) {
	a.owner = uuid.NewString()
	every := a.cfg.Lease / 3
	for {
		held, err := a.cfg.DB.ClaimShard(a.owner, time.Now(), a.cfg.Lease)
		if err != nil {
			return nil, fmt.Errorf("agent: claim the shard: %w", err)
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
	}
	a.cfg.DB.Fence(a.owner)
	slog.Info("shard claimed", "owner", a.owner)
	held, lost := context.WithCancel(ctx)
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		defer lost()
		a.keepShard(held, every)
	}()
	return held, nil
}

// keepShard renews the claim until the agent stops. Storage that fails is retried while the claim runs; once it has
// run out, or another process holds the shard, this process must not go on.
func (a *Agent) keepShard(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	until := time.Now().Add(a.cfg.Lease)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		held, err := a.cfg.DB.ClaimShard(a.owner, now, a.cfg.Lease)
		if held {
			until = now.Add(a.cfg.Lease)
			continue
		}
		if err != nil && now.Before(until) {
			slog.Warn("shard lease not renewed", "err", err)
			continue
		}
		slog.Error("shard lost", "owner", a.owner, "err", err)
		if a.cfg.OnLeaseLost != nil {
			a.cfg.OnLeaseLost()
		}
		return
	}
}

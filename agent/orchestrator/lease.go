package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// claimShard waits until this process holds the shard, and returns the context that ends when it no longer does.
func (o *Orchestrator) claimShard(ctx context.Context) (context.Context, error) {
	o.owner = uuid.NewString()
	every := o.cfg.Lease / 3
	for {
		held, err := o.cfg.DB.ClaimShard(o.owner, time.Now(), o.cfg.Lease)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: claim the shard: %w", err)
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
	o.cfg.DB.Fence(o.owner)
	slog.Info("shard claimed", "owner", o.owner)
	held, lost := context.WithCancel(ctx)
	o.done.Add(1)
	go func() {
		defer o.done.Done()
		defer lost()
		o.keepShard(held, every)
	}()
	return held, nil
}

// keepShard renews the claim until the orchestrator stops. Storage that fails is retried while the claim runs; once it has
// run out, or another process holds the shard, this process must not go on.
func (o *Orchestrator) keepShard(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	until := time.Now().Add(o.cfg.Lease)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		held, err := o.cfg.DB.ClaimShard(o.owner, now, o.cfg.Lease)
		if held {
			until = now.Add(o.cfg.Lease)
			continue
		}
		if err != nil && now.Before(until) {
			slog.Warn("shard lease not renewed", "err", err)
			continue
		}
		slog.Error("shard lost", "owner", o.owner, "err", err)
		if o.cfg.OnLeaseLost != nil {
			o.cfg.OnLeaseLost()
		}
		return
	}
}

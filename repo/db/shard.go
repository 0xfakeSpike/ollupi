package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrShardLost = errors.New("storage: another process holds the shard")

// ExpiresAt is in Unix milliseconds.
type ShardLeaseRow struct {
	ID        int    `gorm:"primaryKey;autoIncrement:false;check:shard_lease_one,id = 1"`
	Owner     string `gorm:"not null"`
	ExpiresAt int64  `gorm:"not null"`
}

func (ShardLeaseRow) TableName() string { return "shard_lease" }

func (d *DB) claimShard(owner string, now time.Time, ttl time.Duration) (bool, error) {
	res := d.gorm.Exec(`INSERT INTO shard_lease(id, owner, expires_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner = excluded.owner, expires_at = excluded.expires_at
		WHERE shard_lease.owner = excluded.owner OR shard_lease.expires_at <= ?`, owner, now.Add(ttl).UnixMilli(), now.UnixMilli())
	return res.RowsAffected == 1, res.Error
}

// At once, for the next process not to wait out what is left of the lease.
func (d *DB) releaseShard(owner string) error {
	return d.gorm.Where("owner = ?", owner).Delete(&ShardLeaseRow{}).Error
}

// So a process that stalled past its lease cannot write over the one that took the shard.
func (d *DB) fence(owner string) { d.lease.owner.Store(&owner) }

// Inside the transaction gorm runs each write in, so the lease cannot change hands between check and write: SQLite's
// one writer gives that; Postgres would need FOR SHARE. Exec is not fenced; only the lease and migrations use it.
func (d *DB) fenceWrites() error {
	fence := func(g *gorm.DB) {
		owner := d.lease.owner.Load()
		if owner == nil || g.Error != nil || g.Statement.Table == (ShardLeaseRow{}).TableName() {
			return
		}
		var holder string
		if err := g.Session(&gorm.Session{NewDB: true}).Model(&ShardLeaseRow{}).Where("id = 1").Select("owner").Scan(&holder).Error; err != nil {
			g.AddError(fmt.Errorf("storage: check the shard lease: %w", err))
			return
		}
		if holder != *owner {
			g.AddError(ErrShardLost)
		}
	}
	const after, name = "gorm:begin_transaction", "repo:fence"
	cb := d.gorm.Callback()
	return errors.Join(
		cb.Create().After(after).Register(name, fence),
		cb.Update().After(after).Register(name, fence),
		cb.Delete().After(after).Register(name, fence),
	)
}

type shardLease struct {
	owner atomic.Pointer[string]
	stop  context.CancelFunc
	done  sync.WaitGroup
}

// Claim waits until the shard is free, takes it and keeps it. The context it returns ends, and onLost is called, once
// another process holds the shard: every write fails from then on, so this process must stop.
func (d *DB) Claim(ctx context.Context, ttl time.Duration, onLost func()) (context.Context, error) {
	owner := uuid.NewString()
	every := ttl / 3
	for {
		held, err := d.claimShard(owner, time.Now(), ttl)
		if err != nil {
			return nil, fmt.Errorf("storage: claim the shard: %w", err)
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
	d.fence(owner)
	held, stop := context.WithCancel(ctx)
	d.lease.stop = stop
	d.lease.done.Add(1)
	go func() {
		defer d.lease.done.Done()
		defer stop()
		d.keepShard(held, owner, ttl, every, onLost)
	}()
	return held, nil
}

// Storage that fails is retried while the claim runs; once it has run out, or another process holds the shard, this
// process must not go on.
func (d *DB) keepShard(ctx context.Context, owner string, ttl, every time.Duration, onLost func()) {
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
		held, err := d.claimShard(owner, now, ttl)
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

func (d *DB) Release() {
	owner := d.lease.owner.Load()
	if owner == nil {
		return
	}
	d.lease.stop()
	d.lease.done.Wait()
	d.releaseShard(*owner) // a claim not released lapses with its lease
}

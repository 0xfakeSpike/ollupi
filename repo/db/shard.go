package db

import (
	"errors"
	"fmt"
	"time"

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

func (d *DB) ClaimShard(owner string, now time.Time, ttl time.Duration) (bool, error) {
	res := d.gorm.Exec(`INSERT INTO shard_lease(id, owner, expires_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner = excluded.owner, expires_at = excluded.expires_at
		WHERE shard_lease.owner = excluded.owner OR shard_lease.expires_at <= ?`, owner, now.Add(ttl).UnixMilli(), now.UnixMilli())
	return res.RowsAffected == 1, res.Error
}

// At once, for the next process not to wait out what is left of the lease.
func (d *DB) ReleaseShard(owner string) error {
	return d.gorm.Where("owner = ?", owner).Delete(&ShardLeaseRow{}).Error
}

// So a process that stalled past its lease cannot write over the one that took the shard.
func (d *DB) Fence(owner string) { d.owner.Store(&owner) }

// Inside the transaction gorm runs each write in, so the lease cannot change hands between check and write: SQLite's
// one writer gives that; Postgres would need FOR SHARE. Exec is not fenced; only the lease and migrations use it.
func (d *DB) fenceWrites() error {
	fence := func(g *gorm.DB) {
		owner := d.owner.Load()
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

package repo

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ErrShardLost says another process runs this shard now, so this one may write no more.
var ErrShardLost = errors.New("storage: another process holds the shard")

// ShardLeaseRow says which process runs this shard, and until when in Unix milliseconds unless it renews.
type ShardLeaseRow struct {
	ID        int    `gorm:"primaryKey;autoIncrement:false;check:shard_lease_one,id = 1"`
	Owner     string `gorm:"not null"`
	ExpiresAt int64  `gorm:"not null"`
}

func (ShardLeaseRow) TableName() string { return "shard_lease" }

// ClaimShard takes the lease, or renews it for its owner, and says false while another owner's lease runs.
func (d *DB) ClaimShard(owner string, now time.Time, ttl time.Duration) (bool, error) {
	res := d.gorm.Exec(`INSERT INTO shard_lease(id, owner, expires_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner = excluded.owner, expires_at = excluded.expires_at
		WHERE shard_lease.owner = excluded.owner OR shard_lease.expires_at <= ?`, owner, now.Add(ttl).UnixMilli(), now.UnixMilli())
	return res.RowsAffected == 1, res.Error
}

// ReleaseShard gives the lease up at once, for the next process not to wait out what is left of it.
func (d *DB) ReleaseShard(owner string) error {
	return d.gorm.Where("owner = ?", owner).Delete(&ShardLeaseRow{}).Error
}

// Fence makes every write on this handle, and on the handles made from it, fail with ErrShardLost once owner no
// longer holds the lease, so a process that stalled past its lease cannot write over the one that took the shard.
func (d *DB) Fence(owner string) { d.owner.Store(&owner) }

// fenceWrites puts the fence before every create, update and delete, inside the transaction gorm runs each in, so that
// the lease cannot change hands between the check and the write. SQLite gives that from its one writer: a transaction
// that read the lease before another process's claim committed cannot write. Postgres would need the read to lock the
// lease row (FOR SHARE). Exec is not fenced; only the lease and migrations write with it.
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

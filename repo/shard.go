package repo

import (
	"errors"
	"time"
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

// Fence makes every transaction on this handle fail once owner no longer holds the lease, so a process that stalled
// past its lease cannot write over the one that took the shard.
func (d *DB) Fence(owner string) { d.owner = owner }

func (d *DB) checkFence() error {
	if d.owner == "" {
		return nil
	}
	var holder string
	if err := d.gorm.Model(&ShardLeaseRow{}).Where("id = 1").Select("owner").Scan(&holder).Error; err != nil {
		return err
	}
	if holder != d.owner {
		return ErrShardLost
	}
	return nil
}

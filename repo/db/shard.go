package db

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrShardLost = errors.New("storage: a later holder of the shard writes to it now")

// One row: the epoch of the shard's latest holder, which only grows.
type ShardEpochRow struct {
	ID    int   `gorm:"primaryKey;autoIncrement:false;check:shard_epoch_one,id = 1"`
	Epoch int64 `gorm:"not null"`
}

func (ShardEpochRow) TableName() string { return "shard_epoch" }

// Fence writes epoch as the shard's, unless a later holder's is there, and from then on refuses every write of this
// handle, and of those made from it, once a later epoch is written: a process that stalled past its lease cannot
// write over the one that took the shard.
func (d *DB) Fence(epoch int64) error {
	res := d.gorm.Exec(`INSERT INTO shard_epoch(id, epoch) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET epoch = excluded.epoch WHERE shard_epoch.epoch <= excluded.epoch`, epoch)
	if res.Error != nil {
		return fmt.Errorf("storage: fence the shard: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrShardLost
	}
	d.epoch.Store(epoch)
	return nil
}

// Inside the transaction gorm runs each write in, so the epoch cannot change between check and write: SQLite's one
// writer gives that; Postgres would need FOR SHARE. Exec is not fenced; only Fence and migrations use it.
func (d *DB) fenceWrites() error {
	fence := func(g *gorm.DB) {
		mine := d.epoch.Load()
		if mine == 0 || g.Error != nil || g.Statement.Table == (ShardEpochRow{}).TableName() {
			return
		}
		var current int64
		if err := g.Session(&gorm.Session{NewDB: true}).Model(&ShardEpochRow{}).Where("id = 1").Select("epoch").Scan(&current).Error; err != nil {
			g.AddError(fmt.Errorf("storage: check the shard's epoch: %w", err))
			return
		}
		if current != mine {
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

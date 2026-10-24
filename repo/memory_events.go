package repo

import (
	"errors"
	"fmt"
	"math"

	"gorm.io/gorm"
)

type MemoryEventRow struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	WorkspaceID string `gorm:"not null;default:'';index:memory_events_scope"`
	SourceTable string `gorm:"not null"`
	SourceID    string `gorm:"not null"`
	Operation   string `gorm:"not null;default:'upsert'"`
	Metadata    string `gorm:"not null;default:'{}'"`
	Attempts    int    `gorm:"not null;default:0"`
	NextAttempt int64  `gorm:"not null;default:0"`
	LastError   string `gorm:"not null;default:''"`
}

func (MemoryEventRow) TableName() string { return "memory_events" }

func (d *DB) NextEventScope(nowMillis int64) (scope string, ok bool, err error) {
	var row MemoryEventRow
	err = d.gorm.Where("next_attempt <= ?", nowMillis).Order("id").Limit(1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	return row.WorkspaceID, err == nil, err
}

func (d *DB) QueuedEvents(scope string, nowMillis int64, limit int) ([]MemoryEventRow, error) {
	var rows []MemoryEventRow
	err := d.gorm.Where("workspace_id = ? AND next_attempt <= ?", scope, nowMillis).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

func (d *DB) AckEvent(id int64) error {
	res := d.gorm.Delete(&MemoryEventRow{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("memory: event %d no longer exists", id)
	}
	return nil
}

const maxEventAttempts = 10

func (d *DB) DeferEvent(id int64, message string, nowMillis int64) (bool, error) {
	res := d.gorm.Model(&MemoryEventRow{}).Where("id = ?", id).Updates(map[string]any{
		"next_attempt": gorm.Expr("CASE WHEN attempts + 1 >= ? THEN ? ELSE ? + MIN(60, 1 << MIN(attempts, 6)) * 60000 END", maxEventAttempts, int64(math.MaxInt64), nowMillis),
		"attempts":     gorm.Expr("attempts + 1"),
		"last_error":   message,
	})
	return res.RowsAffected == 1, res.Error
}

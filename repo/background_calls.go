package repo

import (
	"time"
)

// A BackgroundCallRow is a tool call that runs apart from the turn that made it: the turn goes on, and the call's
// result comes back to its thread when it ends.
type BackgroundCallRow struct {
	ID       string     `gorm:"primaryKey" json:"id"`
	ThreadID string     `gorm:"not null;index:background_calls_thread" json:"threadId"`
	Thread   *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	// ItemID is the call that started it.
	ItemID string `gorm:"not null" json:"itemId"`
	Tool   string `gorm:"not null" json:"tool"`
	// background_calls_running finds the calls still running, as a process that starts does to end them.
	Status      BackgroundStatus `gorm:"not null;index:background_calls_running,where:status = 'running'" json:"status"`
	Error       string           `gorm:"not null;default:''" json:"error,omitempty"`
	StartedAt   time.Time        `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time       `json:"completedAt,omitempty"`
	// The columns that can be large come last.
	Arguments string      `gorm:"not null;default:''" json:"arguments,omitempty"`
	Result    *ToolResult `gorm:"serializer:json" json:"result,omitempty"`
}

func (BackgroundCallRow) TableName() string { return "background_calls" }

type BackgroundStatus string

const (
	BackgroundRunning   BackgroundStatus = "running"
	BackgroundCompleted BackgroundStatus = "completed"
	BackgroundFailed    BackgroundStatus = "failed"
	BackgroundCancelled BackgroundStatus = "cancelled"
	// BackgroundInterrupted is a call whose process stopped before it ended.
	BackgroundInterrupted BackgroundStatus = "interrupted"
)

var BackgroundStatusValues = []BackgroundStatus{BackgroundRunning, BackgroundCompleted, BackgroundFailed, BackgroundCancelled, BackgroundInterrupted}

func (d *DB) StartBackgroundCall(call BackgroundCallRow) error {
	call.Status, call.StartedAt, call.CompletedAt = BackgroundRunning, call.StartedAt.UTC(), nil
	return d.gorm.Create(&call).Error
}

// EndBackgroundCall records how a running call ended. It reports false for a call that had already ended, as one
// cancelled while its tool was still returning has.
func (d *DB) EndBackgroundCall(id string, status BackgroundStatus, result *ToolResult, failure string, at time.Time) (bool, error) {
	at = at.UTC()
	res := d.gorm.Model(&BackgroundCallRow{}).Where("id = ? AND status = ?", id, BackgroundRunning).
		Select("status", "result", "error", "completed_at").
		Updates(&BackgroundCallRow{Status: status, Result: result, Error: failure, CompletedAt: &at})
	return res.RowsAffected == 1, res.Error
}

func (d *DB) BackgroundCall(id string) (BackgroundCallRow, error) {
	var row BackgroundCallRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

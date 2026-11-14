package repo

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// A BackgroundCallRow is a tool call that runs apart from the turn that made it: the turn goes on, and the call's
// result comes back to its thread when it ends.
type BackgroundCallRow struct {
	ID string `gorm:"primaryKey" json:"id"`
	// background_calls_undelivered finds a thread's ended calls whose results have not come back to it; its condition
	// is undeliveredCall, word for word.
	ThreadID string     `gorm:"not null;index:background_calls_thread;index:background_calls_undelivered,where:delivered_at IS NULL AND (status = 'completed' OR status = 'failed' OR status = 'interrupted')" json:"threadId"`
	Thread   *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	// ItemID is the call that started it.
	ItemID string `gorm:"not null" json:"itemId"`
	Tool   string `gorm:"not null" json:"tool"`
	// background_calls_running finds the calls still running, as a process that starts does to end them.
	Status      BackgroundStatus `gorm:"not null;index:background_calls_running,where:status = 'running'" json:"status"`
	Error       string           `gorm:"not null;default:''" json:"error,omitempty"`
	StartedAt   time.Time        `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time       `json:"completedAt,omitempty"`
	// DeliveredAt is when the call's result came back to its thread; a cancelled call's never does.
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
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

// StartBackgroundCall records a call that starts running, unless the user already runs max of them.
func (d *DB) StartBackgroundCall(user string, call BackgroundCallRow, max int) error {
	call.Status, call.StartedAt, call.CompletedAt = BackgroundRunning, call.StartedAt.UTC(), nil
	return d.Tx(func(tx *DB) error {
		var running int64
		if err := tx.gorm.Model(&BackgroundCallRow{}).
			Where("status = ? AND thread_id IN (SELECT id FROM threads WHERE user_id = ?)", BackgroundRunning, user).
			Count(&running).Error; err != nil {
			return err
		}
		if running >= int64(max) {
			return fmt.Errorf("%d background calls are running, the most at once; run this call in the foreground, or wait for one to end", running)
		}
		return tx.gorm.Create(&call).Error
	})
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

// InterruptBackgroundCalls ends as interrupted every call still running, as a process that starts finds the calls of
// one that stopped, and returns them.
func (d *DB) InterruptBackgroundCalls(at time.Time) ([]BackgroundCallRow, error) {
	var calls []BackgroundCallRow
	err := d.Tx(func(tx *DB) error {
		if err := tx.gorm.Where("status = ?", BackgroundRunning).Find(&calls).Error; err != nil {
			return err
		}
		for i := range calls {
			if _, err := tx.EndBackgroundCall(calls[i].ID, BackgroundInterrupted, nil, "the process stopped before the call ended", at); err != nil {
				return err
			}
		}
		return nil
	})
	return calls, err
}

// RunningBackgroundCalls are the ids of the calls still running in the thread and its subagents' threads.
func (d *DB) RunningBackgroundCalls(threadID string) ([]string, error) {
	var ids []string
	err := d.gorm.Model(&BackgroundCallRow{}).
		Where("status = ? AND (thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_thread_id = ?))", BackgroundRunning, threadID, threadID).
		Pluck("id", &ids).Error
	return ids, err
}

const undeliveredCall = "delivered_at IS NULL AND (status = 'completed' OR status = 'failed' OR status = 'interrupted')"

// DeliverBackgroundCalls brings the results of the thread's ended background calls back to it, into its current turn.
// A thread with no current turn takes them in next, a turn they start; without next they are dropped, as a subagent's
// thread that has reported takes none. It reports the turn they went into and whether it started there.
func (d *DB) DeliverBackgroundCalls(threadID string, next *TurnRow) (ThreadRow, TurnRow, []ItemRow, bool, error) {
	var thread ThreadRow
	var turn TurnRow
	var delivered []ItemRow
	started := false
	err := d.Tx(func(tx *DB) error {
		var err error
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		var calls []BackgroundCallRow
		if err := tx.gorm.Where("thread_id = ? AND "+undeliveredCall, threadID).Order("completed_at").Find(&calls).Error; err != nil {
			return err
		}
		if len(calls) == 0 {
			return nil
		}
		now := time.Now().UTC()
		ids := make([]string, len(calls))
		for i, c := range calls {
			ids[i] = c.ID
		}
		if err := tx.gorm.Model(&BackgroundCallRow{}).Where("id IN ?", ids).Update("delivered_at", now).Error; err != nil {
			return err
		}
		turn, err = tx.ActiveTurn(threadID)
		switch {
		case errors.Is(err, ErrNotFound) && next == nil:
			return nil
		case errors.Is(err, ErrNotFound):
			var total int64
			if err := tx.gorm.Model(&TurnRow{}).Where("thread_id = ?", threadID).Count(&total).Error; err != nil {
				return err
			}
			turn = *next
			turn.ThreadID, turn.Seq, turn.Status, turn.CreatedAt, turn.StartedAt = threadID, int(total)+1, TurnReady, now, now
			if err := tx.createTurn(turn); err != nil {
				return err
			}
			if err := tx.touchThread(threadID, now); err != nil {
				return err
			}
			thread.UpdatedAt, started = now, true
		case err != nil:
			return err
		}
		for _, c := range calls {
			status := ItemCompleted
			if c.Status != BackgroundCompleted {
				status = ItemFailed
			}
			it, _, err := tx.saveItemOf(threadID, ItemRow{
				ID: uuid.NewString(), TurnID: turn.ID, Type: ItemBackgroundResult, Status: status, Tool: c.Tool, BackgroundID: c.ID,
				Text: string(c.Status), Error: c.Error, Result: c.Result, StartedAt: now, CompletedAt: &now,
			})
			if err != nil {
				return err
			}
			delivered = append(delivered, it)
		}
		return nil
	})
	return thread, turn, delivered, started, err
}

// UndeliveredBackgroundThreads are the threads with ended calls whose results have not come back to them.
func (d *DB) UndeliveredBackgroundThreads() ([]string, error) {
	var ids []string
	err := d.gorm.Model(&BackgroundCallRow{}).Where(undeliveredCall).Distinct().Pluck("thread_id", &ids).Error
	return ids, err
}

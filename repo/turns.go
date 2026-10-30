package repo

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/google/uuid"
)

type Usage struct {
	InputTokens       int64 `gorm:"not null" json:"inputTokens"`
	CachedInputTokens int64 `gorm:"not null" json:"cachedInputTokens"`
	// CacheWriteInputTokens counts input tokens written to the provider's prompt cache; they are part of InputTokens.
	CacheWriteInputTokens int64 `gorm:"not null" json:"cacheWriteInputTokens"`
	OutputTokens          int64 `gorm:"not null" json:"outputTokens"`
}

func (u *Usage) Add(v Usage) {
	u.InputTokens += v.InputTokens
	u.CachedInputTokens += v.CachedInputTokens
	u.CacheWriteInputTokens += v.CacheWriteInputTokens
	u.OutputTokens += v.OutputTokens
}

type TurnRow struct {
	ID          string       `gorm:"primaryKey;index:turns_ended,priority:2" json:"id"`
	ThreadID    string       `gorm:"not null;uniqueIndex:turns_thread_seq,priority:1;uniqueIndex:turns_one_active,where:status = 'inProgress';index:turns_open,where:status = 'inProgress' OR status = 'queued'" json:"threadId"`
	Thread      *ThreadRow   `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq         int          `gorm:"not null;uniqueIndex:turns_thread_seq,priority:2" json:"seq"`
	Status      TurnStatus   `gorm:"not null" json:"status"`
	Model       llm.Model    `gorm:"not null;serializer:json;check:turns_model,json_valid(model)" json:"model"`
	Usage       Usage        `gorm:"embedded" json:"usage"`
	Error       string       `gorm:"not null;default:''" json:"error,omitempty"`
	Input       string       `gorm:"not null;default:''" json:"input,omitempty"`
	Attachments []Attachment `gorm:"serializer:json" json:"attachments,omitempty"`
	CronJobID   string       `gorm:"not null;default:'';index:turns_cron_job,priority:1" json:"cronJobId,omitempty"`
	StartedAt   time.Time    `gorm:"not null;index:turns_cron_job,priority:2" json:"startedAt"`
	// turns_ended orders the ended turns for memory extraction; its condition is endedTurn, word for word.
	CompletedAt *time.Time `gorm:"index:turns_ended,priority:1,where:(status = 'completed' OR status = 'failed' OR status = 'interrupted') AND completed_at IS NOT NULL" json:"completedAt,omitempty"`
}

func (TurnRow) TableName() string { return "turns" }

type TurnStatus string

const (
	TurnQueued      TurnStatus = "queued"
	TurnInProgress  TurnStatus = "inProgress"
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	TurnFailed      TurnStatus = "failed"
)

var TurnStatusValues = []TurnStatus{TurnQueued, TurnInProgress, TurnCompleted, TurnInterrupted, TurnFailed}

func (s TurnStatus) Value() (driver.Value, error) { return enumValue(s, TurnStatusValues) }

func (d *DB) OpenThreads(exclude []string) ([]string, error) {
	q := d.gorm.Table("turns").Joins("JOIN threads ON threads.id = turns.thread_id").
		Where("(turns.status = 'inProgress' OR turns.status = 'queued')")
	if len(exclude) > 0 {
		q = q.Where("turns.thread_id NOT IN ?", exclude)
	}
	var ids []string
	err := q.Group("turns.thread_id").Order("threads.parent_thread_id IS NOT NULL, MIN(turns.started_at), turns.thread_id").Pluck("turns.thread_id", &ids).Error
	return ids, err
}

func (d *DB) HasQueuedTurn(threadID, cronJobID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&TurnRow{}).Where("thread_id = ? AND status = 'queued' AND cron_job_id = ?", threadID, cronJobID).Count(&n).Error
	return n > 0, err
}

func (d *DB) createTurn(row TurnRow) error {
	row.StartedAt, row.CompletedAt = row.StartedAt.UTC(), nullTime(row.CompletedAt)
	return d.gorm.Create(&row).Error
}

func (d *DB) updateTurn(row TurnRow) error {
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND thread_id = ?", row.ID, row.ThreadID).Updates(map[string]any{
		"status": row.Status, "input_tokens": row.Usage.InputTokens, "cached_input_tokens": row.Usage.CachedInputTokens,
		"cache_write_input_tokens": row.Usage.CacheWriteInputTokens, "output_tokens": row.Usage.OutputTokens, "error": row.Error, "completed_at": nullTime(row.CompletedAt),
	})
	if err := checkOne(res); err != nil {
		return fmt.Errorf("turn %s: %w", row.ID, err)
	}
	return nil
}

func (d *DB) ActiveTurn(threadID string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Where("thread_id = ? AND status = 'inProgress'", threadID).Take(&row).Error)
}

func (d *DB) Turn(id string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

func (d *DB) EndedTurn(id string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Where("id = ? AND status IN ('completed', 'failed', 'interrupted')", id).Take(&row).Error)
}

func (d *DB) SubmitTurn(threadID string, turn TurnRow) (ThreadRow, TurnRow, *ItemRow, error) {
	var thread ThreadRow
	var message *ItemRow
	err := d.Tx(func(tx *DB) error {
		var err error
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		if turn.ID == "" || (strings.TrimSpace(turn.Input) == "" && len(turn.Attachments) == 0) {
			return fmt.Errorf("a new turn needs an id and an input")
		}
		var total, open int64
		if err := tx.gorm.Model(&TurnRow{}).Where("thread_id = ?", threadID).Count(&total).Error; err != nil {
			return err
		}
		if err := tx.gorm.Model(&TurnRow{}).Where("thread_id = ? AND (status = 'inProgress' OR status = 'queued')", threadID).Count(&open).Error; err != nil {
			return err
		}
		turn.ThreadID, turn.Seq, turn.Status = threadID, int(total)+1, TurnQueued
		if err := tx.createTurn(turn); err != nil {
			return err
		}
		if open > 0 {
			return nil
		}
		var it ItemRow
		thread, turn, it, err = tx.startIn(thread, turn)
		message = &it
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, fmt.Errorf("storage: submit turn: %w", err)
	}
	return thread, turn, message, nil
}

func (d *DB) StartQueuedTurn(threadID string) (ThreadRow, TurnRow, ItemRow, error) {
	var thread ThreadRow
	var turn TurnRow
	var message ItemRow
	err := d.Tx(func(tx *DB) error {
		var err error
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		var queued TurnRow
		if err := notFound(tx.gorm.Where("thread_id = ? AND status = 'queued'", threadID).Order("seq").Take(&queued).Error); err != nil {
			return err
		}
		thread, turn, message, err = tx.startIn(thread, queued)
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, fmt.Errorf("storage: start queued turn: %w", err)
	}
	return thread, turn, message, nil
}

func (d *DB) startIn(t ThreadRow, turn TurnRow) (ThreadRow, TurnRow, ItemRow, error) {
	now := time.Now().UTC()
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND status = 'queued'", turn.ID).Updates(map[string]any{"status": TurnInProgress, "started_at": now})
	if err := checkOne(res); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, fmt.Errorf("turn %s: %w", turn.ID, err)
	}
	turn.Status, turn.StartedAt = TurnInProgress, now
	message := NewUserMessage(uuid.NewString(), turn.ID, turn.Input, turn.Attachments, now)
	if err := d.saveItemOf(t.ID, message); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	stored, err := d.Item(message.ID)
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	t.UpdatedAt = now
	if err := d.touchThread(t.ID, now); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	return t, turn, stored, nil
}

func (d *DB) AddUsage(threadID, turnID string, u Usage) error {
	if u == (Usage{}) {
		return nil
	}
	return d.Tx(func(tx *DB) error {
		row, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) || (err == nil && row.ID != turnID) {
			return fmt.Errorf("storage: turn %s is not in progress", turnID)
		}
		if err != nil {
			return err
		}
		row.Usage.Add(u)
		return tx.updateTurn(row)
	})
}

func (d *DB) EndTurn(threadID string, turn TurnRow) (ThreadRow, TurnRow, []ItemRow, error) {
	var thread ThreadRow
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		active, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) || (err == nil && active.ID != turn.ID) {
			return fmt.Errorf("turn %s is not in progress", turn.ID)
		}
		if err != nil {
			return err
		}
		if turn.Status == TurnInProgress {
			return fmt.Errorf("turn %s must end with a final status", turn.ID)
		}
		now := time.Now().UTC()
		turn.ThreadID, turn.Seq, turn.CompletedAt = threadID, active.Seq, &now
		if err := tx.updateTurn(turn); err != nil {
			return err
		}
		var open []ItemRow
		if err := tx.gorm.Where("thread_id = ? AND turn_id = ? AND status IN ('pending', 'inProgress')", threadID, turn.ID).Order("seq").Find(&open).Error; err != nil {
			return err
		}
		for _, it := range open {
			it.Status = ItemFailed
			it.Error = fmt.Sprintf("the turn ended (%s) before this item finished", turn.Status)
			it.CompletedAt = &now
			if err := tx.saveItemOf(threadID, it); err != nil {
				return err
			}
			closed = append(closed, it)
		}
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		thread.UpdatedAt = now
		return tx.touchThread(threadID, now)
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, fmt.Errorf("storage: end turn: %w", err)
	}
	return thread, turn, closed, nil
}

func (d *DB) FinalAnswer(threadID, turnID string) (string, error) {
	var row ItemRow
	err := notFound(d.gorm.Where("thread_id = ? AND turn_id = ? AND type = 'agentMessage' AND status = 'completed'", threadID, turnID).Order("seq DESC").Take(&row).Error)
	return row.Text, err
}

const (
	compactedItem = "type = 'contextCompaction' AND status = 'completed'"
	// carriedItem marks the items before the latest compaction whose effect outlasts it: user messages, which the
	// compacted history quotes; calls allowed for the conversation; and loaded tools and skills, which stay active.
	carriedItem = "type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))))"
)

// RunItems reads, in order, the items a run works from: every item from the thread's latest completed compaction
// on, and before it only the carried ones, without their native records. A run that needs more of the earlier
// items must widen carriedItem and its index. INDEXED BY makes a condition that drifted from its index an error.
func (d *DB) RunItems(threadID string) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.Tx(func(tx *DB) error {
		var from int
		if err := tx.gorm.Table("items INDEXED BY items_compactions").Where("thread_id = ? AND "+compactedItem, threadID).Select("COALESCE(MAX(seq), 0)").Scan(&from).Error; err != nil {
			return err
		}
		if err := tx.gorm.Table("items INDEXED BY items_carried").Omit("native").Where("thread_id = ? AND seq < ? AND ("+carriedItem+")", threadID, from).Order("seq").Find(&rows).Error; err != nil {
			return err
		}
		var recent []ItemRow
		if err := tx.gorm.Where("thread_id = ? AND seq >= ?", threadID, from).Order("seq").Find(&recent).Error; err != nil {
			return err
		}
		rows = append(rows, recent...)
		return nil
	})
	return rows, err
}

func (d *DB) TurnItems(turnID string) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.gorm.Where("turn_id = ?", turnID).Order("seq").Find(&rows).Error
	return rows, err
}

func (d *DB) InputAfterAnswer(turnID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&ItemRow{}).Where("turn_id = ? AND type = 'userMessage' AND seq > (SELECT COALESCE(MAX(seq), 0) FROM items WHERE turn_id = ? AND type = 'agentMessage' AND status = 'completed')", turnID, turnID).Count(&n).Error
	return n > 0, err
}

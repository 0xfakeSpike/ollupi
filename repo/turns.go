package repo

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/google/uuid"
)

type Usage struct {
	InputTokens       int64 `gorm:"not null" json:"inputTokens"`
	CachedInputTokens int64 `gorm:"not null" json:"cachedInputTokens"`
	OutputTokens      int64 `gorm:"not null" json:"outputTokens"`
}

func (u *Usage) Add(v Usage) {
	u.InputTokens += v.InputTokens
	u.CachedInputTokens += v.CachedInputTokens
	u.OutputTokens += v.OutputTokens
}

type TurnRow struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	ThreadID    string     `gorm:"not null;uniqueIndex:turns_thread_seq,priority:1;uniqueIndex:turns_one_active,where:status = 'inProgress';index:turns_open,where:status = 'inProgress' OR status = 'queued'" json:"threadId"`
	Thread      *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq         int        `gorm:"not null;uniqueIndex:turns_thread_seq,priority:2" json:"seq"`
	Status      TurnStatus `gorm:"not null;check:turns_status,status IN ('queued','inProgress','completed','failed','interrupted')" json:"status"`
	Model       llm.Model  `gorm:"not null;serializer:json;check:turns_model,json_valid(model)" json:"model"`
	Usage       Usage      `gorm:"embedded" json:"usage"`
	Error       string     `gorm:"not null;default:''" json:"error,omitempty"`
	Input       string     `gorm:"not null;default:''" json:"input,omitempty"`
	CronJobID   string     `gorm:"not null;default:''" json:"cronJobId,omitempty"`
	StartedAt   time.Time  `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
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

func (d *DB) ActiveThreadIDs() ([]string, error) {
	var ids []string
	err := d.gorm.Model(&TurnRow{}).Where("status = 'inProgress' OR status = 'queued'").Distinct("thread_id").Pluck("thread_id", &ids).Error
	return ids, err
}

func (d *DB) HasQueuedTurn(threadID, cronJobID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&TurnRow{}).Where("thread_id = ? AND status = 'queued' AND cron_job_id = ?", threadID, cronJobID).Count(&n).Error
	return n > 0, err
}

func (d *DB) CreateTurn(row TurnRow) error {
	row.StartedAt, row.CompletedAt = row.StartedAt.UTC(), nullTime(row.CompletedAt)
	return d.gorm.Create(&row).Error
}

func (d *DB) UpdateTurn(row TurnRow) error {
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND thread_id = ?", row.ID, row.ThreadID).Updates(map[string]any{
		"status": row.Status, "input_tokens": row.Usage.InputTokens, "cached_input_tokens": row.Usage.CachedInputTokens,
		"output_tokens": row.Usage.OutputTokens, "error": row.Error, "completed_at": nullTime(row.CompletedAt),
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

func (d *DB) SubmitTurn(threadID string, turn TurnRow) (TurnRow, *ItemRow, error) {
	var message *ItemRow
	err := d.Tx(func(tx *DB) error {
		t, err := tx.ThreadRow(threadID)
		if err != nil {
			return err
		}
		if turn.ID == "" || strings.TrimSpace(turn.Input) == "" {
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
		if err := tx.CreateTurn(turn); err != nil {
			return err
		}
		if open > 0 {
			return nil
		}
		started, it, err := tx.startIn(t, turn)
		turn, message = started, &it
		return err
	})
	if err != nil {
		return TurnRow{}, nil, fmt.Errorf("storage: submit turn: %w", err)
	}
	return turn, message, nil
}

func (d *DB) StartQueuedTurn(threadID string) (TurnRow, ItemRow, error) {
	var turn TurnRow
	var message ItemRow
	err := d.Tx(func(tx *DB) error {
		t, err := tx.ThreadRow(threadID)
		if err != nil {
			return err
		}
		var queued TurnRow
		if err := notFound(tx.gorm.Where("thread_id = ? AND status = 'queued'", threadID).Order("seq").Take(&queued).Error); err != nil {
			return err
		}
		turn, message, err = tx.startIn(t, queued)
		return err
	})
	if err != nil {
		return TurnRow{}, ItemRow{}, fmt.Errorf("storage: start queued turn: %w", err)
	}
	return turn, message, nil
}

func (d *DB) startIn(t ThreadRow, turn TurnRow) (TurnRow, ItemRow, error) {
	now := time.Now().UTC()
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND status = 'queued'", turn.ID).Updates(map[string]any{"status": TurnInProgress, "started_at": now})
	if err := checkOne(res); err != nil {
		return TurnRow{}, ItemRow{}, fmt.Errorf("turn %s: %w", turn.ID, err)
	}
	turn.Status, turn.StartedAt = TurnInProgress, now
	message := NewUserMessage(uuid.NewString(), turn.ID, turn.Input, now)
	if err := d.saveItemOf(t.ID, message); err != nil {
		return TurnRow{}, ItemRow{}, err
	}
	stored, err := d.Item(message.ID)
	if err != nil {
		return TurnRow{}, ItemRow{}, err
	}
	if t.Preview == "" {
		t.Preview = preview(turn.Input)
	}
	t.UpdatedAt = now
	return turn, stored, d.UpdateThread(t)
}

func (d *DB) AddUsage(threadID, turnID string, u Usage) error {
	if u == (Usage{}) {
		return nil
	}
	return d.Tx(func(tx *DB) error {
		row, err := tx.ActiveTurn(threadID)
		if IsNotFound(err) || (err == nil && row.ID != turnID) {
			return fmt.Errorf("storage: turn %s is not in progress", turnID)
		}
		if err != nil {
			return err
		}
		row.Usage.Add(u)
		return tx.UpdateTurn(row)
	})
}

func (d *DB) EndTurn(threadID string, turn TurnRow) (TurnRow, []ItemRow, error) {
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		active, err := tx.ActiveTurn(threadID)
		if IsNotFound(err) || (err == nil && active.ID != turn.ID) {
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
		if err := tx.UpdateTurn(turn); err != nil {
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
		t, err := tx.ThreadRow(threadID)
		if err != nil {
			return err
		}
		t.UpdatedAt = now
		return tx.UpdateThread(t)
	})
	if err != nil {
		return TurnRow{}, nil, fmt.Errorf("storage: end turn: %w", err)
	}
	return turn, closed, nil
}

func (d *DB) FinalAnswer(threadID, turnID string) (string, error) {
	var row ItemRow
	err := notFound(d.gorm.Where("thread_id = ? AND turn_id = ? AND type = 'agentMessage' AND status = 'completed'", threadID, turnID).Order("seq DESC").Take(&row).Error)
	return row.Text, err
}

func (d *DB) InputAfter(threadID, turnID string, seq int) (bool, error) {
	var n int64
	err := d.gorm.Model(&ItemRow{}).Where("thread_id = ? AND turn_id = ? AND seq > ? AND type = 'userMessage'", threadID, turnID, seq).Count(&n).Error
	return n > 0, err
}

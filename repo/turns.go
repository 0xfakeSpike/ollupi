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
	ID          string             `gorm:"primaryKey" json:"id"`
	ThreadID    string             `gorm:"not null;uniqueIndex:turns_thread_seq,priority:1" json:"threadId"`
	Thread      *ThreadRow         `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq         int                `gorm:"not null;uniqueIndex:turns_thread_seq,priority:2" json:"seq"`
	Status      string             `gorm:"not null" json:"status"`
	Model       llm.Model          `gorm:"not null;serializer:json;check:turns_model,json_valid(model)" json:"model"`
	Usage       Usage              `gorm:"embedded" json:"usage"`
	Error       string             `gorm:"not null;default:''" json:"error,omitempty"`
	Settings    PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:turns_settings,json_valid(settings)" json:"settings"`
	Input       string             `gorm:"not null;default:''" json:"input,omitempty"`
	CronJobID   string             `gorm:"not null;default:''" json:"cronJobId,omitempty"`
	StartedAt   time.Time          `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time         `json:"completedAt,omitempty"`
}

func (TurnRow) TableName() string { return "turns" }

const (
	TurnQueued      = "queued"
	TurnInProgress  = "inProgress"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
	TurnFailed      = "failed"
)

func (d *DB) ActiveThreadIDs() ([]string, error) {
	var ids []string
	err := d.gorm.Model(&TurnRow{}).Where("status IN ('inProgress', 'queued')").Distinct("thread_id").Pluck("thread_id", &ids).Error
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
		v, err := tx.Thread(threadID)
		if err != nil {
			return err
		}
		if turn.ID == "" || strings.TrimSpace(turn.Input) == "" {
			return fmt.Errorf("a new turn needs an id and an input")
		}
		turn.ThreadID, turn.Seq, turn.Status = threadID, len(v.Turns)+1, TurnQueued
		if err := tx.CreateTurn(turn); err != nil {
			return err
		}
		if _, active := v.ActiveTurn(); active || len(v.QueuedTurns()) > 0 {
			return nil
		}
		v.Turns = append(v.Turns, turn)
		started, it, err := tx.startIn(v, turn)
		turn, message = started, &it
		return err
	})
	if err != nil {
		return TurnRow{}, nil, fmt.Errorf("storage: submit turn: %w", err)
	}
	return turn, message, nil
}

func (d *DB) StartQueuedTurn(threadID string) (TurnRow, ItemRow, bool, error) {
	var turn TurnRow
	var message ItemRow
	started := false
	err := d.Tx(func(tx *DB) error {
		v, err := tx.Thread(threadID)
		if err != nil {
			return err
		}
		queued := v.QueuedTurns()
		if _, active := v.ActiveTurn(); active || len(queued) == 0 {
			return nil
		}
		turn, message, err = tx.startIn(v, queued[0])
		started = err == nil
		return err
	})
	if err != nil {
		return TurnRow{}, ItemRow{}, false, fmt.Errorf("storage: start queued turn: %w", err)
	}
	return turn, message, started, nil
}

func (d *DB) startIn(v ThreadView, turn TurnRow) (TurnRow, ItemRow, error) {
	now := time.Now().UTC()
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND status = 'queued'", turn.ID).Updates(map[string]any{"status": TurnInProgress, "started_at": now})
	if err := checkOne(res); err != nil {
		return TurnRow{}, ItemRow{}, fmt.Errorf("turn %s: %w", turn.ID, err)
	}
	turn.Status, turn.StartedAt = TurnInProgress, now
	message := NewUserMessage(uuid.NewString(), turn.ID, turn.Input, now)
	message.CronJobID = turn.CronJobID
	if err := d.saveItemOf(v.Thread.ID, message); err != nil {
		return TurnRow{}, ItemRow{}, err
	}
	stored, err := d.Item(message.ID)
	if err != nil {
		return TurnRow{}, ItemRow{}, err
	}
	t := v.Thread
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

func (d *DB) EndTurn(threadID string, turn TurnRow) ([]ItemRow, error) {
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		var err error
		closed, err = tx.endTurnIn(threadID, turn)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("storage: end turn: %w", err)
	}
	return closed, nil
}

func (d *DB) EndSubagentTurn(threadID string, turn TurnRow, report ItemRow) (closed []ItemRow, stored ItemRow, err error) {
	err = d.Tx(func(tx *DB) error {
		child, err := tx.Thread(threadID)
		if err != nil {
			return err
		}
		parentID := child.Thread.ParentThreadID
		active, err := tx.ActiveTurn(parentID)
		if IsNotFound(err) || (err == nil && (active.ID != report.TurnID || report.ID != child.Thread.ParentItemID)) {
			return fmt.Errorf("item %s is not the Agent call of thread %s's turn in progress", report.ID, parentID)
		}
		if err != nil {
			return err
		}
		if closed, err = tx.endTurnIn(threadID, turn); err != nil {
			return err
		}
		if err := tx.saveItemOf(parentID, report); err != nil {
			return err
		}
		stored, err = tx.Item(report.ID)
		return err
	})
	if err != nil {
		return nil, ItemRow{}, fmt.Errorf("storage: end subagent turn: %w", err)
	}
	return closed, stored, nil
}

func (d *DB) endTurnIn(threadID string, turn TurnRow) ([]ItemRow, error) {
	v, err := d.Thread(threadID)
	if err != nil {
		return nil, err
	}
	active, ok := v.ActiveTurn()
	if !ok || active.ID != turn.ID {
		return nil, fmt.Errorf("turn %s is not in progress", turn.ID)
	}
	if turn.Status == TurnInProgress {
		return nil, fmt.Errorf("turn %s must end with a final status", turn.ID)
	}
	now := time.Now().UTC()
	turn.ThreadID, turn.Seq, turn.CompletedAt = threadID, active.Seq, &now
	if err := d.UpdateTurn(turn); err != nil {
		return nil, err
	}
	var closed []ItemRow
	for _, it := range v.Items {
		if it.TurnID != turn.ID || it.Status != ItemInProgress {
			continue
		}
		it.Status = ItemFailed
		it.Error = fmt.Sprintf("the turn ended (%s) before this item finished", turn.Status)
		it.CompletedAt = &now
		if err := d.saveItemOf(threadID, it); err != nil {
			return nil, err
		}
		closed = append(closed, it)
	}
	t := v.Thread
	t.UpdatedAt = now
	return closed, d.UpdateThread(t)
}

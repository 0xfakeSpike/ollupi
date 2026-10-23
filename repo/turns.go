package repo

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
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
	ID             string          `gorm:"primaryKey" json:"id"`
	ThreadID       string          `gorm:"not null;uniqueIndex:turns_thread_seq,priority:1" json:"threadId"`
	Thread         *ThreadRow      `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq            int             `gorm:"not null;uniqueIndex:turns_thread_seq,priority:2" json:"seq"`
	Status         string          `gorm:"not null" json:"status"`
	Model          llm.Model       `gorm:"not null;serializer:json;check:turns_model,json_valid(model)" json:"model"`
	ApprovalPolicy string          `gorm:"not null" json:"approvalPolicy"`
	Usage          Usage           `gorm:"embedded" json:"usage"`
	Error          string          `gorm:"not null;default:''" json:"error,omitempty"`
	Permissions    PermissionRules `gorm:"not null;default:'{}';serializer:json;check:turns_permissions,json_valid(permissions)" json:"permissions,omitempty"`
	StartedAt      time.Time       `gorm:"not null" json:"startedAt"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
}

func (TurnRow) TableName() string { return "turns" }

const (
	TurnInProgress  = "inProgress"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
	TurnFailed      = "failed"
)

func (d *DB) ActiveThreadIDs() ([]string, error) {
	var ids []string
	err := d.gorm.Model(&TurnRow{}).Where("status = 'inProgress'").Order("started_at, id").Pluck("thread_id", &ids).Error
	return ids, err
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
	return row, notFound(d.gorm.Where("id = ? AND status <> 'inProgress'", id).Take(&row).Error)
}

func (d *DB) BeginTurn(threadID string, turn TurnRow, input []ItemRow) (TurnRow, error) {
	err := d.Tx(func(tx *DB) error {
		v, err := tx.Thread(threadID)
		if err != nil {
			return err
		}
		if active, ok := v.ActiveTurn(); ok {
			return fmt.Errorf("thread %s already has turn %s in progress", threadID, active.ID)
		}
		if turn.ID == "" || turn.Status != TurnInProgress {
			return fmt.Errorf("a new turn needs an id and status inProgress")
		}
		turn.ThreadID, turn.Seq = threadID, len(v.Turns)+1
		if err := tx.CreateTurn(turn); err != nil {
			return err
		}
		t := v.Thread
		for _, it := range input {
			if it.Type == ItemUserMessage && t.Preview == "" {
				t.Preview = preview(it.Text)
			}
			if err := tx.saveItemOf(threadID, it); err != nil {
				return err
			}
		}
		t.UpdatedAt = time.Now().UTC()
		return tx.UpdateThread(t)
	})
	if err != nil {
		return TurnRow{}, fmt.Errorf("storage: begin turn: %w", err)
	}
	return turn, nil
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

package repo

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ThreadRow struct {
	ID             string             `gorm:"primaryKey" json:"id"`
	WorkspaceID    Ref                `gorm:"index:threads_workspace_updated,priority:1" json:"workspaceId,omitempty"`
	Workspace      *WorkspaceRow      `gorm:"foreignKey:WorkspaceID;constraint:OnDelete:RESTRICT" json:"-"`
	Preview        string             `gorm:"not null" json:"preview"`
	Settings       PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:threads_settings,json_valid(settings)" json:"settings"`
	ParentThreadID Ref                `gorm:"index" json:"parentThreadId,omitempty"`
	Parent         *ThreadRow         `gorm:"foreignKey:ParentThreadID;constraint:OnDelete:CASCADE" json:"-"`
	ParentItemID   Ref                `json:"parentItemId,omitempty"`
	CreatedAt      time.Time          `gorm:"not null;autoCreateTime:false" json:"createdAt"`
	UpdatedAt      time.Time          `gorm:"not null;autoUpdateTime:false;index;index:threads_workspace_updated,priority:2,sort:desc" json:"updatedAt"`
}

func (ThreadRow) TableName() string { return "threads" }

func (d *DB) Thread(id string) (ThreadView, error) {
	var out ThreadView
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ?", id).Take(&out.Thread).Error); err != nil {
			return err
		}
		if err := tx.gorm.Where("thread_id = ?", id).Order("seq").Find(&out.Turns).Error; err != nil {
			return err
		}
		return tx.gorm.Where("thread_id = ?", id).Order("seq").Find(&out.Items).Error
	})
	if err != nil {
		return ThreadView{}, err
	}
	for i, it := range out.Items {
		if it.Seq != i+1 {
			return ThreadView{}, fmt.Errorf("storage: thread %s items have a gap at seq %d", id, i+1)
		}
	}
	return out, nil
}

func (d *DB) ThreadPage(id string, before, limit int) (ThreadView, bool, error) {
	var out ThreadView
	more := false
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ?", id).Take(&out.Thread).Error); err != nil {
			return err
		}
		if err := tx.gorm.Where("thread_id = ?", id).Order("seq").Find(&out.Turns).Error; err != nil {
			return err
		}
		q := tx.gorm.Where("thread_id = ?", id)
		if before > 0 {
			q = q.Where("seq < ?", before)
		}
		if err := q.Order("seq DESC").Limit(limit + 1).Find(&out.Items).Error; err != nil {
			return err
		}
		if len(out.Items) > limit {
			more, out.Items = true, out.Items[:limit]
		}
		slices.Reverse(out.Items)
		return nil
	})
	if err != nil {
		return ThreadView{}, false, err
	}
	return out, more, nil
}

func (d *DB) ThreadRow(id string) (ThreadRow, error) {
	var row ThreadRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

type ThreadFilter struct {
	WorkspaceID *string `json:"workspaceId,omitempty"`
	Limit       int     `json:"limit,omitempty"`
}

func (d *DB) ThreadRows(f ThreadFilter) ([]ThreadRow, error) {
	q := d.gorm.Order("updated_at DESC, created_at DESC")
	if f.WorkspaceID != nil {
		if *f.WorkspaceID == "" {
			q = q.Where("workspace_id IS NULL")
		} else {
			q = q.Where("workspace_id = ?", *f.WorkspaceID)
		}
	}
	q = q.Where("parent_thread_id IS NULL")
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	var rows []ThreadRow
	return rows, q.Find(&rows).Error
}

func (d *DB) CreateThread(row ThreadRow) error {
	row.CreatedAt, row.UpdatedAt = row.CreatedAt.UTC(), row.UpdatedAt.UTC()
	return d.gorm.Create(&row).Error
}

func (d *DB) UpdateThread(row ThreadRow) error {
	res := d.gorm.Model(&ThreadRow{}).Where("id = ?", row.ID).Updates(map[string]any{
		"preview": row.Preview, "updated_at": row.UpdatedAt.UTC(),
	})
	if err := checkOne(res); err != nil {
		return fmt.Errorf("thread %s: %w", row.ID, err)
	}
	return nil
}

func (d *DB) SetThreadSettings(id string, settings PermissionSettings) (ThreadRow, error) {
	raw, err := json.Marshal(settings)
	if err != nil {
		return ThreadRow{}, fmt.Errorf("thread %s: encode settings: %w", id, err)
	}
	var row ThreadRow
	err = d.Tx(func(tx *DB) error {
		res := tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("settings", string(raw))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

type ThreadStatus string

const (
	ThreadIdle               ThreadStatus = "idle"
	ThreadActive             ThreadStatus = "active"
	ThreadWaitingOnApproval  ThreadStatus = "waitingOnApproval"
	ThreadWaitingOnUserInput ThreadStatus = "waitingOnUserInput"
)

type ThreadSummary struct {
	ThreadRow
	Status ThreadStatus `json:"status"`
}

type ThreadView struct {
	Thread ThreadRow `json:"thread"`
	Turns  []TurnRow `json:"turns"`
	Items  []ItemRow `json:"items"`
}

func (v ThreadView) Status() ThreadStatus {
	if _, active := v.ActiveTurn(); !active {
		return ThreadIdle
	}
	it, waiting := v.PendingRequest()
	switch {
	case !waiting:
		return ThreadActive
	case !it.WaitsForApproval():
		return ThreadWaitingOnUserInput
	default:
		return ThreadWaitingOnApproval
	}
}

func (v ThreadView) ActiveTurn() (TurnRow, bool) {
	for i := len(v.Turns) - 1; i >= 0; i-- {
		if v.Turns[i].Status == TurnInProgress {
			return v.Turns[i], true
		}
	}
	return TurnRow{}, false
}

func (v ThreadView) QueuedTurns() []TurnRow {
	var out []TurnRow
	for _, t := range v.Turns {
		if t.Status == TurnQueued {
			out = append(out, t)
		}
	}
	return out
}

func (v ThreadView) AgentCalls() []ItemRow {
	turn, ok := v.ActiveTurn()
	if !ok {
		return nil
	}
	var out []ItemRow
	for _, it := range v.Items {
		if it.TurnID == turn.ID && it.WaitsOnAgent() {
			out = append(out, it)
		}
	}
	return out
}

func (v ThreadView) FinalAnswer(turnID string) (string, bool) {
	for i := len(v.Items) - 1; i >= 0; i-- {
		if it := v.Items[i]; it.TurnID == turnID && it.Type == ItemAgentMessage && it.Status == ItemCompleted {
			return it.Text, true
		}
	}
	return "", false
}

func (v ThreadView) InputAfter(turnID string, seq int) bool {
	for _, it := range v.Items {
		if it.TurnID == turnID && it.Seq > seq && it.Type == ItemUserMessage {
			return true
		}
	}
	return false
}

func (v ThreadView) PendingRequest() (ItemRow, bool) {
	if p := v.PendingRequests(); len(p) > 0 {
		return p[0], true
	}
	return ItemRow{}, false
}

func (v ThreadView) PendingRequests() []ItemRow {
	turn, ok := v.ActiveTurn()
	if !ok {
		return nil
	}
	var out []ItemRow
	for _, it := range v.Items {
		if it.TurnID == turn.ID && it.IsPending() {
			out = append(out, it)
		}
	}
	return out
}

func preview(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if runes := []rune(line); len(runes) > 80 {
		return string(runes[:80])
	}
	return line
}

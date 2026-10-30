package repo

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type ThreadRow struct {
	ID             string             `gorm:"primaryKey" json:"id"`
	WorkspaceID    Ref                `gorm:"index:threads_workspace_updated,priority:1" json:"workspaceId,omitempty"`
	Workspace      *WorkspaceRow      `gorm:"foreignKey:WorkspaceID;constraint:OnDelete:RESTRICT" json:"-"`
	Title          string             `gorm:"not null" json:"title"`
	Settings       PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:threads_settings,json_valid(settings)" json:"settings"`
	ParentThreadID Ref                `gorm:"index" json:"parentThreadId,omitempty"`
	Parent         *ThreadRow         `gorm:"foreignKey:ParentThreadID;constraint:OnDelete:CASCADE" json:"-"`
	ParentItemID   Ref                `json:"parentItemId,omitempty"`
	CreatedAt      time.Time          `gorm:"not null;autoCreateTime:false" json:"createdAt"`
	UpdatedAt      time.Time          `gorm:"not null;autoUpdateTime:false;index;index:threads_workspace_updated,priority:2,sort:desc" json:"updatedAt"`
	ArchivedAt     *time.Time         `json:"archivedAt,omitempty"`
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
	WorkspaceID *string       `json:"workspaceId,omitempty"`
	Archived    bool          `json:"archived,omitempty"`
	After       *ThreadCursor `json:"after,omitempty"`
	Limit       int           `json:"limit,omitempty"`
}

// ThreadCursor is a thread's place in list order: the last thread of a page, after which the next page starts.
type ThreadCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func (d *DB) ThreadRows(f ThreadFilter) ([]ThreadRow, error) {
	q := d.gorm.Order("updated_at DESC, created_at DESC, id DESC")
	if c := f.After; c != nil {
		up, created := c.UpdatedAt.UTC(), c.CreatedAt.UTC()
		q = q.Where("updated_at < ? OR (updated_at = ? AND (created_at < ? OR (created_at = ? AND id < ?)))", up, up, created, created, c.ID)
	}
	if f.WorkspaceID != nil {
		if *f.WorkspaceID == "" {
			q = q.Where("workspace_id IS NULL")
		} else {
			q = q.Where("workspace_id = ?", *f.WorkspaceID)
		}
	}
	q = q.Where("parent_thread_id IS NULL")
	if f.Archived {
		q = q.Where("archived_at IS NOT NULL")
	} else {
		q = q.Where("archived_at IS NULL")
	}
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
		"title": row.Title, "updated_at": row.UpdatedAt.UTC(),
	})
	if err := checkOne(res); err != nil {
		return fmt.Errorf("thread %s: %w", row.ID, err)
	}
	return nil
}

func (d *DB) SetThreadTitle(id, title string) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("title", title)); err != nil {
			return fmt.Errorf("thread %s: %w", id, err)
		}
		var err error
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

func (d *DB) SetThreadArchived(id string, at *time.Time) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("archived_at", nullTime(at))); err != nil {
			return fmt.Errorf("thread %s: %w", id, err)
		}
		var err error
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

func (d *DB) DeleteThread(id string) error {
	res := d.gorm.Delete(&ThreadRow{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
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

var ThreadStatusValues = []ThreadStatus{ThreadIdle, ThreadActive, ThreadWaitingOnApproval, ThreadWaitingOnUserInput}

type ThreadSummary struct {
	ThreadRow
	Status ThreadStatus `json:"status"`
}

type ThreadView struct {
	Thread ThreadRow `json:"thread"`
	Turns  []TurnRow `json:"turns"`
	Items  []ItemRow `json:"items"`
}

func (v ThreadView) ActiveTurn() (TurnRow, bool) {
	return activeTurn(v.Turns)
}

type ThreadState struct {
	Thread    ThreadRow
	Turns     []TurnRow
	OpenCalls []ItemRow
}

func (d *DB) ThreadState(id string) (ThreadState, error) {
	var out ThreadState
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ?", id).Take(&out.Thread).Error); err != nil {
			return err
		}
		if err := tx.gorm.Where("thread_id = ?", id).Order("seq").Find(&out.Turns).Error; err != nil {
			return err
		}
		turn, active := out.ActiveTurn()
		if !active {
			return nil
		}
		return tx.gorm.Where("thread_id = ? AND turn_id = ? AND type = 'toolCall' AND status IN ('pending', 'inProgress')", id, turn.ID).Order("seq").Find(&out.OpenCalls).Error
	})
	if err != nil {
		return ThreadState{}, err
	}
	return out, nil
}

func (s ThreadState) ActiveTurn() (TurnRow, bool) {
	return activeTurn(s.Turns)
}

func (s ThreadState) QueuedTurns() []TurnRow {
	var out []TurnRow
	for _, t := range s.Turns {
		if t.Status == TurnQueued {
			out = append(out, t)
		}
	}
	return out
}

func (s ThreadState) PendingRequests() []ItemRow {
	var out []ItemRow
	for _, it := range s.OpenCalls {
		if it.IsPending() {
			out = append(out, it)
		}
	}
	return out
}

func (s ThreadState) AgentCalls() []ItemRow {
	var out []ItemRow
	for _, it := range s.OpenCalls {
		if it.WaitsOnAgent() {
			out = append(out, it)
		}
	}
	return out
}

func (s ThreadState) Status() ThreadStatus {
	if _, active := s.ActiveTurn(); !active {
		return ThreadIdle
	}
	pending := s.PendingRequests()
	switch {
	case len(pending) == 0:
		return ThreadActive
	case !pending[0].WaitsForApproval():
		return ThreadWaitingOnUserInput
	default:
		return ThreadWaitingOnApproval
	}
}

func activeTurn(turns []TurnRow) (TurnRow, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Status == TurnInProgress {
			return turns[i], true
		}
	}
	return TurnRow{}, false
}

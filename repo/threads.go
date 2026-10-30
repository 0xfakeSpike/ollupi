package repo

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type ThreadRow struct {
	ID          string             `gorm:"primaryKey;index:threads_list,priority:3;index:threads_workspace_list,priority:4" json:"id"`
	WorkspaceID Ref                `gorm:"index:threads_workspace_list,priority:1" json:"workspaceId,omitempty"`
	Workspace   *WorkspaceRow      `gorm:"foreignKey:WorkspaceID;constraint:OnDelete:RESTRICT" json:"-"`
	Title       string             `gorm:"not null" json:"title"`
	Settings    PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:threads_settings,json_valid(settings)" json:"settings"`
	// threads_parent finds a thread's subagent threads; top-level threads stay out of it, so lists use threads_list.
	ParentThreadID Ref        `gorm:"index:threads_parent,where:parent_thread_id IS NOT NULL" json:"parentThreadId,omitempty"`
	Parent         *ThreadRow `gorm:"foreignKey:ParentThreadID;constraint:OnDelete:CASCADE" json:"-"`
	ParentItemID   Ref        `json:"parentItemId,omitempty"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime:false;index:threads_list,priority:2;index:threads_workspace_list,priority:3" json:"createdAt"`
	// threads_list and threads_workspace_list hold the top-level threads in list order, read backwards for newest first.
	UpdatedAt  time.Time  `gorm:"not null;autoUpdateTime:false;index:threads_list,priority:1,where:parent_thread_id IS NULL;index:threads_workspace_list,priority:2,where:parent_thread_id IS NULL" json:"updatedAt"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
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
		// The page is for a client, which never sees native records; only the runtime reads them, through Thread.
		q := tx.gorm.Omit("native").Where("thread_id = ?", id)
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

// ThreadRows lists one page of top-level threads, newest first, and whether more follow it.
func (d *DB) ThreadRows(f ThreadFilter) ([]ThreadRow, bool, error) {
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
	var rows []ThreadRow
	if err := q.Limit(f.Limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	if len(rows) > f.Limit {
		return rows[:f.Limit], true, nil
	}
	return rows, false, nil
}

func (d *DB) CreateThread(row ThreadRow) error {
	row.CreatedAt, row.UpdatedAt = row.CreatedAt.UTC(), row.UpdatedAt.UTC()
	return d.gorm.Create(&row).Error
}

// touchThread moves a thread's updated_at; nothing else about the thread changes with a turn.
func (d *DB) touchThread(id string, at time.Time) error {
	if err := checkOne(d.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("updated_at", at.UTC())); err != nil {
		return fmt.Errorf("thread %s: %w", id, err)
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

// ThreadState is what scheduling needs of a thread: its open turns (in progress or queued, in order), its last
// turn, and the unfinished tool calls of the turn in progress. Ended turns other than the last are not read.
type ThreadState struct {
	Thread    ThreadRow
	Open      []TurnRow
	Last      *TurnRow
	OpenCalls []ItemRow
}

// openTurn is the condition of the turns_open index, word for word; INDEXED BY makes a drift an error.
const openTurn = "(status = 'inProgress' OR status = 'queued')"

const openCalls = "type = 'toolCall' AND status IN ('pending', 'inProgress')"

func (d *DB) ThreadState(id string) (ThreadState, error) {
	var out ThreadState
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ?", id).Take(&out.Thread).Error); err != nil {
			return err
		}
		if err := tx.gorm.Table("turns INDEXED BY turns_open").Where("thread_id = ? AND "+openTurn, id).Order("seq").Find(&out.Open).Error; err != nil {
			return err
		}
		var last []TurnRow
		if err := tx.gorm.Where("thread_id = ?", id).Order("seq DESC").Limit(1).Find(&last).Error; err != nil {
			return err
		}
		if len(last) > 0 {
			out.Last = &last[0]
		}
		turn, active := out.ActiveTurn()
		if !active {
			return nil
		}
		return tx.gorm.Where("turn_id = ? AND "+openCalls, turn.ID).Order("seq").Find(&out.OpenCalls).Error
	})
	if err != nil {
		return ThreadState{}, err
	}
	return out, nil
}

func (s ThreadState) ActiveTurn() (TurnRow, bool) {
	return activeTurn(s.Open)
}

func (s ThreadState) QueuedTurns() []TurnRow {
	var out []TurnRow
	for _, t := range s.Open {
		if t.Status == TurnQueued {
			out = append(out, t)
		}
	}
	return out
}

func (s ThreadState) PendingRequests() []ItemRow {
	var out []ItemRow
	for _, it := range s.OpenCalls {
		if it.WaitsOnUser() {
			out = append(out, it)
		}
	}
	return out
}

func (s ThreadState) AgentCalls() []ItemRow {
	var out []ItemRow
	for _, it := range s.OpenCalls {
		if it.waitsOnAgent() {
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

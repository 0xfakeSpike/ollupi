package db

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

const ofUser = "thread_id IN (SELECT id FROM threads WHERE user_id = ?)"

type ThreadRow struct {
	ID            string             `gorm:"primaryKey;index:threads_list,priority:4;index:threads_workspace_list,priority:4" json:"id"`
	UserID        string             `gorm:"not null;default:'';index:threads_list,priority:1;index:threads_user" json:"-"`
	User          *UserRow           `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	WorkspaceID   Ref                `gorm:"index:threads_workspace;index:threads_workspace_list,priority:1" json:"workspaceId,omitempty"`
	Workspace     *WorkspaceRow      `gorm:"foreignKey:WorkspaceID;constraint:OnDelete:RESTRICT" json:"-"`
	EnvironmentID Ref                `gorm:"index:threads_environment" json:"environmentId,omitempty"`
	Environment   *EnvironmentRow    `gorm:"foreignKey:EnvironmentID;constraint:OnDelete:RESTRICT" json:"-"`
	Title         string             `gorm:"not null" json:"title"`
	Settings      PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:threads_settings,json_valid(settings)" json:"settings"`
	// threads_parent finds a thread's subagent threads; top-level threads stay out of it, so lists use threads_list.
	ParentThreadID Ref        `gorm:"index:threads_parent,where:parent_thread_id IS NOT NULL" json:"parentThreadId,omitempty"`
	Parent         *ThreadRow `gorm:"foreignKey:ParentThreadID;constraint:OnDelete:CASCADE" json:"-"`
	ParentItemID   Ref        `json:"parentItemId,omitempty"`
	CreatedAt      time.Time  `gorm:"not null;autoCreateTime:false;index:threads_list,priority:3;index:threads_workspace_list,priority:3" json:"createdAt"`
	// threads_list and threads_workspace_list hold the top-level threads in list order, read backwards for newest first.
	UpdatedAt  time.Time  `gorm:"not null;autoUpdateTime:false;index:threads_list,priority:2,where:parent_thread_id IS NULL;index:threads_workspace_list,priority:2,where:parent_thread_id IS NULL" json:"updatedAt"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	RepliedAt  *time.Time `json:"repliedAt,omitempty"`
	ReadAt     *time.Time `json:"readAt,omitempty"`
}

func (ThreadRow) TableName() string { return "threads" }

func (t ThreadRow) Unread() bool {
	return t.RepliedAt != nil && (t.ReadAt == nil || t.RepliedAt.After(*t.ReadAt))
}

func (d *DB) Thread(id string) (ThreadView, error) {
	var out ThreadView
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ?", id).Take(&out.Thread).Error); err != nil {
			return err
		}
		if err := tx.gorm.Where("thread_id = ?", id).Order("seq").Find(&out.Turns).Error; err != nil {
			return err
		}
		return tx.gorm.Where("thread_id = ?", id).Order(conversationOrder).Find(&out.Items).Error
	})
	if err != nil {
		return ThreadView{}, err
	}
	return out, nil
}

func (d *DB) ThreadPage(user, id string, before, limit int) (ThreadView, bool, error) {
	var out ThreadView
	more := false
	err := d.Tx(func(tx *DB) error {
		if err := notFound(tx.gorm.Where("id = ? AND user_id = ?", id, user).Take(&out.Thread).Error); err != nil {
			return err
		}
		// The page is for a client, which never sees native records; only the runtime reads them.
		q := tx.gorm.Table("items INDEXED BY items_thread_order").Omit("native").Where("thread_id = ?", id)
		if before > 0 {
			q = q.Where("(turn_seq, seq) < (SELECT turn_seq, seq FROM items WHERE thread_id = ? AND seq = ?)", id, before)
		}
		if err := q.Order("turn_seq DESC, seq DESC").Limit(limit + 1).Find(&out.Items).Error; err != nil {
			return err
		}
		if len(out.Items) > limit {
			more, out.Items = true, out.Items[:limit]
		}
		slices.Reverse(out.Items)
		// Only the page's turns and those not ended: a thread of many turns does not send all of them.
		turns := map[string]bool{}
		for _, it := range out.Items {
			turns[it.TurnID] = true
		}
		return tx.gorm.Where("thread_id = ? AND (id IN ? OR "+openTurn+")", id, slices.Collect(maps.Keys(turns))).Order("seq").Find(&out.Turns).Error
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

func (d *DB) ThreadOf(user, id string) (ThreadRow, error) {
	var row ThreadRow
	return row, notFound(d.gorm.Where("id = ? AND user_id = ?", id, user).Take(&row).Error)
}

type ThreadFilter struct {
	WorkspaceID *string       `json:"workspaceId,omitempty"`
	Archived    bool          `json:"archived,omitempty"`
	After       *ThreadCursor `json:"after,omitempty"`
	Limit       int           `json:"limit,omitempty"`
}

type ThreadCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func (d *DB) ThreadRows(user string, f ThreadFilter) ([]ThreadRow, bool, error) {
	q := d.gorm.Where("user_id = ?", user).Order("updated_at DESC, created_at DESC, id DESC")
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

func (d *DB) touchThread(id string, at time.Time) error {
	if err := checkOne(d.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("updated_at", at.UTC())); err != nil {
		return fmt.Errorf("thread %s: %w", id, err)
	}
	return nil
}

func (d *DB) SetThreadTitle(user, id, title string) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		if _, err := tx.ThreadOf(user, id); err != nil {
			return err
		}
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("title", title)); err != nil {
			return fmt.Errorf("thread %s: %w", id, err)
		}
		var err error
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

func (d *DB) MarkThreadRead(user, id string, at time.Time) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		if _, err := tx.ThreadOf(user, id); err != nil {
			return err
		}
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("read_at", at.UTC())); err != nil {
			return fmt.Errorf("thread %s: %w", id, err)
		}
		var err error
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

func (d *DB) SetThreadArchived(user, id string, at *time.Time) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		if _, err := tx.ThreadOf(user, id); err != nil {
			return err
		}
		if at != nil {
			if err := tx.CheckIdle(id); err != nil {
				return err
			}
		}
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("archived_at", nullTime(at))); err != nil {
			return fmt.Errorf("thread %s: %w", id, err)
		}
		var err error
		row, err = tx.ThreadRow(id)
		return err
	})
	return row, err
}

func (d *DB) DeleteThread(user, id string) error {
	return d.Tx(func(tx *DB) error {
		if _, err := tx.ThreadOf(user, id); err != nil {
			return err
		}
		if err := tx.CheckIdle(id); err != nil {
			return err
		}
		return checkOne(tx.gorm.Delete(&ThreadRow{}, "id = ?", id))
	})
}

func (d *DB) CheckIdle(id string) error {
	var open int64
	if err := d.gorm.Model(&TurnRow{}).Where("thread_id = ? AND "+openTurn, id).Count(&open).Error; err != nil {
		return err
	}
	if open > 0 {
		return ErrThreadBusy
	}
	return nil
}

// change sees the settings as they are in the transaction: a change that adds to them does not undo one made since
// its caller read them.
func (d *DB) UpdateThreadSettings(user, id string, change func(PermissionSettings) PermissionSettings) (ThreadRow, error) {
	var row ThreadRow
	err := d.Tx(func(tx *DB) error {
		current, err := tx.ThreadOf(user, id)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(change(current.Settings)) // strings alone: it cannot fail
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", id).Update("settings", string(raw))); err != nil {
			return err
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
	Open      []TurnRow
	Last      *TurnRow
	OpenCalls []ItemRow
}

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

func (s ThreadState) Behind() []TurnRow {
	if len(s.Open) < 2 {
		return nil
	}
	return s.Open[1:]
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

func activeTurn(turns []TurnRow) (TurnRow, bool) {
	for _, t := range turns {
		if !t.Status.Ended() {
			return t, true
		}
	}
	return TurnRow{}, false
}

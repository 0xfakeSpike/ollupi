// Package event holds what the agents tell those who watch their conversations: a conversation, turn or item that
// started, changed or ended, the text the model streams, and what waits on the user.
package event

import (
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

type Kind string

const (
	ThreadChanged       Kind = "thread_changed"
	ThreadDeleted       Kind = "thread_deleted"
	TurnQueued          Kind = "turn_queued"
	TurnStarted         Kind = "turn_started"
	ItemStarted         Kind = "item_started"
	ItemUpdated         Kind = "item_updated"
	ItemCompleted       Kind = "item_completed"
	ItemDelta           Kind = "item_delta"
	ApprovalRequested   Kind = "approval_requested"
	UserInputRequested  Kind = "user_input_requested"
	TurnCompleted       Kind = "turn_completed"
	ThreadStatusChanged Kind = "thread_status_changed"
	WorkspaceChanged    Kind = "workspace_changed"
	EnvironmentChanged  Kind = "environment_changed"
	// Only for a scheduled task archived or deleted with its thread.
	CronJobChanged Kind = "cron_job_changed"
	Error          Kind = "error"
)

type Event struct {
	Kind        Kind
	Thread      db.ThreadRow
	Turn        db.TurnRow
	Item        *db.ItemRow
	Workspace   *db.WorkspaceRow
	Environment *db.EnvironmentRow
	CronJobID   string
	Delta       string
	Status      db.ThreadStatus
	Error       string
}

type Emitter struct {
	DB      *db.DB
	OnEvent func(Event)
}

func (e Emitter) Emit(ev Event) {
	if e.OnEvent != nil {
		e.OnEvent(ev)
	}
}

func (e Emitter) Item(t db.ThreadRow, turn db.TurnRow, it db.ItemRow, isNew bool) {
	unfinished := it.Status == db.ItemPending || it.Status == db.ItemInProgress
	switch {
	case isNew:
		started := it
		e.Emit(Event{Kind: ItemStarted, Thread: t, Turn: turn, Item: &started})
	case unfinished:
		e.Emit(Event{Kind: ItemUpdated, Thread: t, Turn: turn, Item: &it})
	}
	if !unfinished {
		e.Emit(Event{Kind: ItemCompleted, Thread: t, Turn: turn, Item: &it})
	}
}

func (e Emitter) Status(threadID string) {
	t, err := e.DB.ThreadRow(threadID)
	if err != nil {
		return
	}
	if t.ParentThreadID != "" {
		e.Status(string(t.ParentThreadID))
		return
	}
	status, err := ThreadStatus(e.DB, threadID)
	if err != nil {
		return
	}
	e.Emit(Event{Kind: ThreadStatusChanged, Thread: t, Status: status})
}

func (e Emitter) Requests(st db.ThreadState) {
	turn, _ := st.ActiveTurn()
	for _, it := range st.PendingRequests() {
		kind := ApprovalRequested
		if !it.WaitsForApproval() {
			kind = UserInputRequested
		}
		e.Emit(Event{Kind: kind, Thread: st.Thread, Turn: turn, Item: &it})
	}
}

// What, not why: the cause is the storage's own words, with its paths and tables.
func (e Emitter) Fault(threadID, turnID, what string) {
	t, rerr := e.DB.ThreadRow(threadID)
	if rerr != nil {
		t = db.ThreadRow{ID: threadID}
	}
	e.Emit(Event{Kind: Error, Thread: t, Turn: db.TurnRow{ID: turnID, ThreadID: threadID}, Error: what})
}

func ThreadStatus(store *db.DB, id string) (db.ThreadStatus, error) {
	turn, err := store.ActiveTurn(id)
	if errors.Is(err, db.ErrNotFound) {
		return db.ThreadIdle, nil
	}
	if err != nil {
		return "", fmt.Errorf("the status of thread %s: %w", id, err)
	}
	switch turn.Status {
	case db.TurnWaitingOnApproval:
		return db.ThreadWaitingOnApproval, nil
	case db.TurnWaitingOnUserInput:
		return db.ThreadWaitingOnUserInput, nil
	}
	running, err := store.RunningSubagents(turn.ID)
	if err != nil {
		return "", fmt.Errorf("the status of thread %s: %w", id, err)
	}
	for _, child := range running {
		cs, err := ThreadStatus(store, child)
		if err != nil {
			return "", err
		}
		if cs == db.ThreadWaitingOnApproval || cs == db.ThreadWaitingOnUserInput {
			return cs, nil
		}
	}
	return db.ThreadActive, nil
}

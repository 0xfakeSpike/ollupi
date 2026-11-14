// Package event holds what the agents tell those who watch their conversations: a conversation, turn or item that
// started, changed or ended, the text the model streams, and what waits on the user.
package event

import (
	"errors"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

// A Kind is what an event tells.
type Kind string

const (
	ThreadStarted       Kind = "thread_started"
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
	// CronJobChanged reports a scheduled task that changed with its thread: archived with it, or deleted with it.
	CronJobChanged Kind = "cron_job_changed"
	Error          Kind = "error"
)

// An Event is one thing that happened to a conversation, its turns or its items, or to a workspace or environment.
type Event struct {
	Kind      Kind
	Thread    db.ThreadRow
	Turn      db.TurnRow
	Item      *db.ItemRow
	Workspace *db.WorkspaceRow
	// Environment is the environment a conversation can run in, added, renamed or removed.
	Environment *db.EnvironmentRow
	CronJobID   string
	Delta       string
	Status      db.ThreadStatus
	Error       string
}

// An Emitter tells OnEvent, when set, what happened, reading what it tells of from DB.
type Emitter struct {
	DB      *db.DB
	OnEvent func(Event)
}

func (e Emitter) Emit(ev Event) {
	if e.OnEvent != nil {
		e.OnEvent(ev)
	}
}

// Item tells of an item that was recorded: it started, when it is new; it changed, while it is unfinished; and it
// completed, once it is not.
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

// TurnStarted logs a turn that starts and tells of it.
func (e Emitter) TurnStarted(t db.ThreadRow, turn db.TurnRow) {
	slog.Info("turn started", "thread", t.ID, "turn", turn.ID, "model", turn.Model.Name, "connection", turn.Model.Connection,
		"subagent", t.ParentThreadID != "", "cronJob", turn.CronJobID)
	e.Emit(Event{Kind: TurnStarted, Thread: t, Turn: turn})
}

// Status tells of the status of the conversation, or of the one a subagent's conversation belongs to.
func (e Emitter) Status(threadID string) {
	t, err := e.DB.ThreadRow(threadID)
	if err != nil {
		slog.Warn("thread status not sent", "thread", threadID, "err", err)
		return
	}
	if t.ParentThreadID != "" {
		e.Status(string(t.ParentThreadID))
		return
	}
	e.Emit(Event{Kind: ThreadStatusChanged, Thread: t, Status: ThreadStatus(e.DB, threadID)})
}

// Requests tells of what the conversation's turn waits on the user for.
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

// Fault logs what went wrong with a conversation's turn and tells of it.
func (e Emitter) Fault(threadID, turnID string, err error) {
	slog.Error("orchestrator fault", "thread", threadID, "turn", turnID, "err", err)
	t, rerr := e.DB.ThreadRow(threadID)
	if rerr != nil {
		t = db.ThreadRow{ID: threadID}
	}
	e.Emit(Event{Kind: Error, Thread: t, Turn: db.TurnRow{ID: turnID, ThreadID: threadID}, Error: err.Error()})
}

// ThreadStatus reads a conversation's status off its current turn: waiting on a person when the turn is, or when a
// subagent it waits on is, and otherwise active or idle.
func ThreadStatus(store *db.DB, id string) db.ThreadStatus {
	turn, err := store.ActiveTurn(id)
	if errors.Is(err, db.ErrNotFound) {
		return db.ThreadIdle
	}
	if err != nil {
		return ""
	}
	switch turn.Status {
	case db.TurnWaitingOnApproval:
		return db.ThreadWaitingOnApproval
	case db.TurnWaitingOnUserInput:
		return db.ThreadWaitingOnUserInput
	case db.TurnWaitingOnAgent:
		st, err := store.ThreadState(id)
		if err != nil {
			return ""
		}
		for _, call := range st.AgentCalls() {
			if cs := ThreadStatus(store, call.Await.Handle); cs == db.ThreadWaitingOnApproval || cs == db.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return db.ThreadActive
}

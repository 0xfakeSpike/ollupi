package agent

import (
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo"
)

type EventKind string

const (
	EventThreadStarted       EventKind = "thread_started"
	EventThreadChanged       EventKind = "thread_changed"
	EventThreadDeleted       EventKind = "thread_deleted"
	EventTurnQueued          EventKind = "turn_queued"
	EventTurnStarted         EventKind = "turn_started"
	EventItemStarted         EventKind = "item_started"
	EventItemUpdated         EventKind = "item_updated"
	EventItemCompleted       EventKind = "item_completed"
	EventItemDelta           EventKind = "item_delta"
	EventApprovalRequested   EventKind = "approval_requested"
	EventUserInputRequested  EventKind = "user_input_requested"
	EventTurnCompleted       EventKind = "turn_completed"
	EventThreadStatusChanged EventKind = "thread_status_changed"
	EventWorkspaceChanged    EventKind = "workspace_changed"
	EventEnvironmentChanged  EventKind = "environment_changed"
	// EventCronJobChanged reports a scheduled task that changed with its thread: archived with it, or deleted with it.
	EventCronJobChanged EventKind = "cron_job_changed"
	EventError          EventKind = "error"
)

type Event struct {
	Kind      EventKind
	Thread    repo.ThreadRow
	Turn      repo.TurnRow
	Item      *repo.ItemRow
	Workspace *repo.WorkspaceRow
	// Environment is the environment a conversation can run in, added, renamed or removed.
	Environment *repo.EnvironmentRow
	CronJobID   string
	Delta       string
	Status      repo.ThreadStatus
	Error       string
}

func (o *Orchestrator) fault(threadID, turnID string, err error) {
	slog.Error("orchestrator fault", "thread", threadID, "turn", turnID, "err", err)
	t, rerr := o.cfg.DB.ThreadRow(threadID)
	if rerr != nil {
		t = repo.ThreadRow{ID: threadID}
	}
	o.emit(Event{Kind: EventError, Thread: t, Turn: repo.TurnRow{ID: turnID, ThreadID: threadID}, Error: err.Error()})
}

func (o *Orchestrator) emit(e Event) {
	if o.cfg.OnEvent != nil {
		o.cfg.OnEvent(e)
	}
}

func (o *Orchestrator) emitStatus(threadID string) {
	t, err := o.cfg.DB.ThreadRow(threadID)
	if err != nil {
		slog.Warn("thread status not sent", "thread", threadID, "err", err)
		return
	}
	if t.ParentThreadID != "" {
		o.emitStatus(string(t.ParentThreadID))
		return
	}
	status := o.threadStatus(threadID)
	o.emit(Event{Kind: EventThreadStatusChanged, Thread: t, Status: status})
}

func (o *Orchestrator) emitRequests(st repo.ThreadState) {
	turn, _ := st.ActiveTurn()
	for _, it := range st.PendingRequests() {
		kind := EventApprovalRequested
		if !it.WaitsForApproval() {
			kind = EventUserInputRequested
		}
		o.emit(Event{Kind: kind, Thread: st.Thread, Turn: turn, Item: &it})
	}
}

func (o *Orchestrator) emitItem(t repo.ThreadRow, turn repo.TurnRow, it repo.ItemRow, isNew bool) {
	unfinished := it.Status == repo.ItemPending || it.Status == repo.ItemInProgress
	switch {
	case isNew:
		started := it
		o.emit(Event{Kind: EventItemStarted, Thread: t, Turn: turn, Item: &started})
	case unfinished:
		o.emit(Event{Kind: EventItemUpdated, Thread: t, Turn: turn, Item: &it})
	}
	if !unfinished {
		o.emit(Event{Kind: EventItemCompleted, Thread: t, Turn: turn, Item: &it})
	}
}

// startedTurn logs a turn that starts and tells the clients.
func (o *Orchestrator) startedTurn(t repo.ThreadRow, turn repo.TurnRow) {
	slog.Info("turn started", "thread", t.ID, "turn", turn.ID, "model", turn.Model.Name, "connection", turn.Model.Connection,
		"subagent", t.ParentThreadID != "", "cronJob", turn.CronJobID)
	o.emit(Event{Kind: EventTurnStarted, Thread: t, Turn: turn})
}

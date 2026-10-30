package agent

import (
	"log"

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
	CronJobID string
	Delta     string
	Status    repo.ThreadStatus
	Error     string
}

func (a *Agent) fault(threadID, turnID string, err error) {
	log.Printf("agent: thread %s turn %s: %v", threadID, turnID, err)
	t, rerr := a.cfg.DB.ThreadRow(threadID)
	if rerr != nil {
		t = repo.ThreadRow{ID: threadID}
	}
	a.emit(Event{Kind: EventError, Thread: t, Turn: repo.TurnRow{ID: turnID, ThreadID: threadID}, Error: err.Error()})
}

func (a *Agent) emit(e Event) {
	if a.cfg.OnEvent != nil {
		a.cfg.OnEvent(e)
	}
}

func (a *Agent) emitStatus(threadID string) {
	t, err := a.cfg.DB.ThreadRow(threadID)
	if err != nil {
		log.Printf("agent: thread %s: status not sent: %v", threadID, err)
		return
	}
	if t.ParentThreadID != "" {
		a.emitStatus(string(t.ParentThreadID))
		return
	}
	status := a.threadStatus(threadID)
	a.emit(Event{Kind: EventThreadStatusChanged, Thread: t, Status: status})
}

func (a *Agent) emitRequests(st repo.ThreadState) {
	turn, _ := st.ActiveTurn()
	for _, it := range st.PendingRequests() {
		kind := EventApprovalRequested
		if !it.WaitsForApproval() {
			kind = EventUserInputRequested
		}
		a.emit(Event{Kind: kind, Thread: st.Thread, Turn: turn, Item: &it})
	}
}

func (a *Agent) emitItem(t repo.ThreadRow, turn repo.TurnRow, it repo.ItemRow, isNew bool) {
	unfinished := it.Status == repo.ItemPending || it.Status == repo.ItemInProgress
	switch {
	case isNew:
		started := it
		a.emit(Event{Kind: EventItemStarted, Thread: t, Turn: turn, Item: &started})
	case unfinished:
		a.emit(Event{Kind: EventItemUpdated, Thread: t, Turn: turn, Item: &it})
	}
	if !unfinished {
		a.emit(Event{Kind: EventItemCompleted, Thread: t, Turn: turn, Item: &it})
	}
}

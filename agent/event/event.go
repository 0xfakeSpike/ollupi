// Package event holds what the agents tell those who watch their threads: a thread, turn or item that started,
// changed or ended, and the text the model streams.
package event

import "github.com/0xfakeSpike/ollupi/repo"

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

// An Event is one thing that happened to a thread, its turns or its items.
type Event struct {
	Kind      Kind
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

// OfItem are the events of an item that was recorded: it started, when it is new; it changed, while it is unfinished;
// and it completed, once it is not.
func OfItem(t repo.ThreadRow, turn repo.TurnRow, it repo.ItemRow, isNew bool) []Event {
	var out []Event
	unfinished := it.Status == repo.ItemPending || it.Status == repo.ItemInProgress
	switch {
	case isNew:
		started := it
		out = append(out, Event{Kind: ItemStarted, Thread: t, Turn: turn, Item: &started})
	case unfinished:
		changed := it
		out = append(out, Event{Kind: ItemUpdated, Thread: t, Turn: turn, Item: &changed})
	}
	if !unfinished {
		done := it
		out = append(out, Event{Kind: ItemCompleted, Thread: t, Turn: turn, Item: &done})
	}
	return out
}

// Package event holds what the agents tell those who watch their conversations: a conversation, turn or item that
// started, changed or ended, the text the model streams, and what waits on the user.
package event

import (
	"sync"

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
	EnvironmentDeleted  Kind = "environment_deleted"
	CronJobChanged      Kind = "cron_job_changed"
	SettingsChanged     Kind = "settings_changed"
	Error               Kind = "error"
)

type Event struct {
	Kind Kind
	// User is whose the event is: only their subscribers hear it.
	User        string
	Thread      db.ThreadRow
	Turn        db.TurnRow
	Item        *db.ItemRow
	Workspace   *db.WorkspaceRow
	Environment *db.EnvironmentRow
	CronJobID   string
	// CronJob is nil once the task is gone.
	CronJob  *db.CronJobView
	Settings *db.SettingsRow
	Delta    string
	Status   db.ThreadStatus
	Error    string
}

// Bus hands each event to the subscribers of its user, and to those of every user, in the goroutine that publishes
// it, so that a subscriber that queues what it hears keeps the order it was published in. A subscriber must not block
// and must not publish: the orchestrator's loop and a running turn publish.
type Bus struct {
	mu     sync.RWMutex
	byUser map[string]map[*Subscription]struct{}
	all    map[*Subscription]struct{}
}

type Subscription struct {
	bus  *Bus
	user string
	all  bool
	fn   func(Event)
}

func New() *Bus {
	return &Bus{byUser: map[string]map[*Subscription]struct{}{}, all: map[*Subscription]struct{}{}}
}

func (b *Bus) Subscribe(user string, fn func(Event)) *Subscription {
	s := &Subscription{bus: b, user: user, fn: fn}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byUser[user] == nil {
		b.byUser[user] = map[*Subscription]struct{}{}
	}
	b.byUser[user][s] = struct{}{}
	return s
}

func (b *Bus) SubscribeAll(fn func(Event)) *Subscription {
	s := &Subscription{bus: b, all: true, fn: fn}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.all[s] = struct{}{}
	return s
}

func (s *Subscription) Close() {
	b := s.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.all {
		delete(b.all, s)
		return
	}
	delete(b.byUser[s.user], s)
	if len(b.byUser[s.user]) == 0 {
		delete(b.byUser, s.user)
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.byUser[e.User] {
		s.fn(e)
	}
	for s := range b.all {
		s.fn(e)
	}
}

func (b *Bus) Item(t db.ThreadRow, turn db.TurnRow, it db.ItemRow, isNew bool) {
	unfinished := it.Status == db.ItemPending || it.Status == db.ItemInProgress
	switch {
	case isNew:
		started := it
		b.Publish(Event{Kind: ItemStarted, User: t.UserID, Thread: t, Turn: turn, Item: &started})
	case unfinished:
		b.Publish(Event{Kind: ItemUpdated, User: t.UserID, Thread: t, Turn: turn, Item: &it})
	}
	if !unfinished {
		b.Publish(Event{Kind: ItemCompleted, User: t.UserID, Thread: t, Turn: turn, Item: &it})
	}
}

func (b *Bus) Status(t db.ThreadRow, status db.ThreadStatus) {
	b.Publish(Event{Kind: ThreadStatusChanged, User: t.UserID, Thread: t, Status: status})
}

func (b *Bus) Requests(st db.ThreadState) {
	turn, _ := st.ActiveTurn()
	for _, it := range st.PendingRequests() {
		kind := ApprovalRequested
		if !it.WaitsForApproval() {
			kind = UserInputRequested
		}
		b.Publish(Event{Kind: kind, User: st.Thread.UserID, Thread: st.Thread, Turn: turn, Item: &it})
	}
}

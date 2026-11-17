// Package store is where a shard keeps what it knows: each conversation's log and the views folded from it, beside the
// headers of the conversations. Every write is a transaction; once it commits, what it changed reaches the feed. The
// package says what a store does; store/sqlite is one.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/user"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrShardLost refuses every write of a process once a later holder of the shard has written its epoch.
	ErrShardLost = errors.New("store: a later holder of the shard writes to it now")
)

type Store interface {
	// Update runs fn in one transaction; what it changed reaches the feed once it commits, and nothing does if fn
	// fails.
	Update(ctx context.Context, fn func(Tx) error) error
	// Read runs fn on one snapshot: what it reads, and the Seq it reads, hold together.
	Read(ctx context.Context, fn func(Reader) error) error
	Feed() *Feed
	// Fence writes epoch as the shard's, unless a later holder's is there, and from then on refuses this process's
	// writes once a later one is written.
	Fence(epoch int64) error
	Close() error
}

type Reader interface {
	Users() ([]string, error)
	Settings(user string) (user.Settings, error)
	Workspaces(user string) ([]user.Workspace, error)
	Workspace(user, id string) (user.Workspace, error)
	// LocalEnvironment, this machine, comes first.
	Environments(user string) ([]user.Environment, error)
	Environment(user, id string) (user.Environment, error)
	EnvironmentInUse(user, id string) (bool, error)
	// WorkspaceBusy says whether a conversation in the workspace has a turn in progress.
	WorkspaceBusy(user, id string) (bool, error)
	MCPServers(user string) ([]user.MCPServer, error)
	CronJobs(user, thread string) ([]user.CronJob, error)
	CronJob(id string) (user.CronJob, error)
	// Due are the scheduled tasks not paused whose next run has come.
	Due(now time.Time) ([]user.CronJob, error)
	// Threads are a user's own conversations, the latest active first; more says whether there are others after them.
	Threads(user string, f ThreadFilter) (threads []Thread, more bool, err error)
	// Children are the conversations of the subagents a conversation launched.
	Children(thread string) ([]Thread, error)
	Search(user string, terms []string, workspace string, limit int) (Found, error)
	// Busy counts the conversations with a turn in progress, and Active the users who wrote since then.
	Busy() (int64, error)
	Active(since time.Time) (int64, error)

	Thread(id string) (Thread, error)
	View(thread string) (conversation.View, error)
	// Page is a thread's items, the latest limit of them before the item at seq before, or the latest when before is
	// 0, with the turns they belong to and every turn not ended; more says whether there are older items.
	Page(thread string, before int64, limit int) (page Page, more bool, err error)
	// Runnable are the threads whose turn in progress can run now, those runnable the longest first and a subagent's
	// after the user's own, leaving out the users in except.
	Runnable(limit int, except []string) ([]Runnable, error)
	// Seq is the latest commit, as the feed numbers them: a view read now holds every change up to it.
	Seq() (int64, error)

	// Memories are a user's notes at places, or at every place when none is given, the latest changed first.
	Memories(user string, places ...memory.Place) ([]memory.Memory, error)
	Memory(user, id string) (memory.Memory, error)
	MemoryState(user string) (memory.State, error)
	// Ended are a user's turns ended after the one at after, in the order memory.Cursor reads them, at most limit.
	Ended(user string, after memory.Cursor, limit int) ([]memory.Cursor, error)
	// Unread counts a user's turns ended after the one at after.
	Unread(user string, after memory.Cursor) (int64, error)
}

type Tx interface {
	Reader
	// EnsureUser makes a user met for the first time ready: their settings, and this machine as an environment.
	EnsureUser(id string) error
	SetSettings(user string, s user.Settings) error
	// PutWorkspace adds or replaces a workspace; two of a user's cannot share a folder.
	PutWorkspace(user string, w user.Workspace) error
	PutEnvironment(user string, e user.Environment) error
	DeleteEnvironment(user, id string) error
	PutMCPServer(user string, s user.MCPServer) error
	DeleteMCPServer(user, name string) error
	PutCronJob(j user.CronJob) error
	DeleteCronJob(id string) error
	// PutMemory adds or replaces a note; two at one place cannot share a topic.
	PutMemory(m memory.Memory) error
	SetMemoryState(user string, s memory.State) error
	// DeleteThread deletes a conversation with its log, its subagents' and its scheduled tasks.
	DeleteThread(id string) error
	CreateThread(t Thread) error
	// UpdateThread writes a thread's header, not what is folded from its log.
	UpdateThread(t Thread) error
	// Append appends entries to a thread's log, each checked against the conversation as the ones before left it, and
	// returns the view they fold into.
	Append(thread string, entries ...conversation.Entry) (conversation.View, error)
}

// Thread is a conversation's header, with what the store folds of its log beside it.
type Thread struct {
	user.Thread
	User string
	Head int64
	// Status is this conversation's own.
	Status   conversation.ThreadStatus
	Runnable bool
}

type Page struct {
	Turns []conversation.Turn
	Items []conversation.Item
}

type Runnable struct {
	Thread string
	User   string
}

// Event is what reaches the feed: what a commit did to one thread or to what a user owns, or a delta of what a model is
// writing now, which is not kept and has no seq. A commit's events share its seq.
type Event struct {
	Seq    int64
	User   string
	Thread string
	Change *Change
	Owned  *Owned
	Delta  *Delta
}

// Owned is a change to what a user owns or sets: one of these, the deleted ones by their id or name.
type Owned struct {
	Settings           *user.Settings
	Workspace          *user.Workspace
	Environment        *user.Environment
	EnvironmentDeleted string
	MCPServer          *user.MCPServer
	MCPServerDeleted   string
	CronJob            *user.CronJob
	CronJobDeleted     string
	Memory             *memory.Memory
}

// Change is what one commit did to one thread: the entries appended to its log and the items and turns they changed,
// as they are now, its header when the commit wrote it, and its status.
type Change struct {
	Entries []conversation.Entry
	Items   []conversation.Item
	Turns   []conversation.Turn
	Header  *Thread
	Deleted bool
	Status  conversation.ThreadStatus
	// Runnable says whether the thread's turn in progress can run now.
	Runnable bool
	At       time.Time
}

type Delta struct {
	Turn      string
	Item      string
	Text      string
	Reasoning string
}

type ThreadFilter struct {
	// Workspace, when not nil, keeps the conversations in that workspace, or those in none when it is "".
	Workspace *string
	Archived  bool
	After     *ThreadCursor
	Limit     int
}

type ThreadCursor struct {
	Updated time.Time
	Created time.Time
	ID      string
}

type Found struct {
	Threads  []Thread
	Messages []Hit
	Tasks    []user.CronJob
}

type Hit struct {
	Thread  string
	Title   string
	Item    string
	Turn    string
	Seq     int64
	Kind    conversation.ItemKind
	Snippet string
	At      time.Time
}

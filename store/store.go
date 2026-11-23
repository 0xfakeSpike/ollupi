package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
)

var (
	ErrNotFound  = errors.New("store: not found")
	ErrShardLost = errors.New("store: a later holder of the shard writes to it now")
)

type Store interface {
	Update(ctx context.Context, fn func(Tx) error) error
	Read(ctx context.Context, fn func(Reader) error) error
	Feed() *Feed
	Fence(epoch int64) error
	Close() error
}

type Reader interface {
	Users() ([]string, error)
	Settings(user string) (account.Settings, error)
	Workspaces(user string) ([]account.Workspace, error)
	Workspace(user, id string) (account.Workspace, error)
	Environments(user string) ([]account.Environment, error)
	Environment(user, id string) (account.Environment, error)
	EnvironmentInUse(user, id string) (bool, error)
	WorkspaceBusy(user, id string) (bool, error)
	MCPServers(user string) ([]account.MCPServer, error)
	CronJobs(user, thread string) ([]CronJobView, error)
	CronJob(id string) (account.CronJob, error)
	Due(now time.Time) ([]account.CronJob, error)
	Threads(user string, f ThreadFilter) (threads []Thread, more bool, err error)
	Waiting(user string) ([]string, error)
	Children(thread string) ([]Thread, error)
	Calls(user, workspace, tool string) ([]conversation.Item, error)
	Todos(user string, f TodoFilter) (todos []account.Todo, more bool, err error)
	Todo(user string, id int64) (account.Todo, error)
	Search(user string, terms []string, workspace string, limit int) (Found, error)
	Busy() (int64, error)
	Active(since time.Time) (int64, error)

	Thread(id string) (Thread, error)
	View(thread string) (conversation.View, error)
	Page(thread string, before int64, limit int) (page Page, more bool, err error)
	Runnable(limit int, except []string) ([]Runnable, error)
	Tasks(limit int, except []string) ([]Runnable, error)
	Task(id string) (Task, error)
	Seq() (int64, error)

	Memories(user string, places ...memory.Place) ([]memory.Memory, error)
	Memory(user, id string) (memory.Memory, error)
	MemoryState(user string) (memory.State, error)
	EndedTurns(user string, after memory.Cursor, limit int) ([]memory.Cursor, error)
	Unread(user string, after memory.Cursor) (int64, error)

	// MissingUsage returns the modelResponded entries that have no usage row
	// yet, in (thread, seq) order after the given key. A thread of "" reads
	// every thread. The body is raw so the caller can skip an entry it cannot
	// read instead of failing the whole batch.
	MissingUsage(thread string, after UsageKey, limit int) (entries []UsageEntry, more bool, err error)
	UsageBuckets(user string, q UsageQuery) ([]UsageBucket, error)
	PresenceBuckets(user string, q PresenceQuery) ([]PresenceBucket, error)
}

type Tx interface {
	Reader
	EnsureUser(id string) error
	SetSettings(user string, s account.Settings) error
	PutWorkspace(user string, w account.Workspace) error
	AddTodo(user string, t account.Todo) (account.Todo, error)
	PutTodo(user string, t account.Todo) error
	PutEnvironment(user string, e account.Environment) error
	DeleteEnvironment(user, id string) error
	PutMCPServer(user string, s account.MCPServer) error
	DeleteMCPServer(user, name string) error
	PutCronJob(j account.CronJob) error
	DeleteCronJob(id string) error
	PutMemory(m memory.Memory) error
	SetMemoryState(user string, s memory.State) error
	PutUsage(u Usage) error
	PutPresence(p Presence) error
	DeleteThread(id string) error
	CreateThread(t Thread) error
	UpdateThread(t Thread) error
	Append(thread string, entries ...conversation.Entry) (conversation.View, error)
	CreateTask(t Task) error
	StartTask(id string, at time.Time) error
	DeleteTask(id string) error
}

type Thread struct {
	account.Thread
	User     string
	Head     int64
	Status   conversation.ThreadStatus
	Runnable bool
}

type Page struct {
	Turns []conversation.Turn
	Items []conversation.Item
}

type Runnable struct {
	ID     string
	User   string
	Thread string
}

type Task struct {
	ID      string
	User    string
	Thread  string
	Turn    string
	Item    string
	Call    json.RawMessage
	Created time.Time
	Started *time.Time
}

// Usage is one model response: a normal answer or a context compaction. It is
// derived from the modelResponded entries by a background consumer, never
// written on the path that appends them.
type Usage struct {
	ID        string
	Thread    string
	Seq       int64
	User      string
	Workspace string
	Turn      string
	Item      string
	At        time.Time
	Source    UsageSource
	Model     string
	Tokens    conversation.Usage
}

type UsageSource string

const (
	UsageResponse   UsageSource = "response"
	UsageCompaction UsageSource = "compaction"
)

// UsageEntry is a modelResponded entry that has no usage row yet.
type UsageEntry struct {
	Thread    string
	Seq       int64
	User      string
	Workspace string
	Turn      string
	Item      string
	At        time.Time
	Body      []byte
}

// UsageKey is a position in the (thread, seq) order of entries.
type UsageKey struct {
	Thread string
	Seq    int64
}

type UsageGroup string

const (
	UsageByDay       UsageGroup = "day"
	UsageByWorkspace UsageGroup = "workspace"
	UsageByModel     UsageGroup = "model"
)

var UsageGroups = []UsageGroup{UsageByDay, UsageByWorkspace, UsageByModel}

type UsageQuery struct {
	From, To time.Time
	Group    UsageGroup
	// OffsetMinutes is how many minutes east of UTC the days of a day grouping
	// start, so a client shows the days of its own time zone.
	OffsetMinutes int
}

type UsageBucket struct {
	Key    string
	Tokens conversation.Usage
}

// Presence is one stretch of time the user spent in the app, as the desktop
// client observed it. The client writes a stretch when it ends and may write
// the same id again with a later end: the row is then extended, never moved.
// Thread and Todo are a snapshot from the moment it is written, so time keeps
// adding up to the same todo after its thread is archived and reopened.
type Presence struct {
	ID        string
	User      string
	Thread    string
	Todo      int64
	Workspace string
	State     PresenceState
	Start     time.Time
	End       time.Time
}

// PresenceState is what the app was showing while a stretch was recorded: the
// thread the user was on, the app without a thread, or neither because the
// app was not in front.
type PresenceState string

const (
	PresenceSeen      PresenceState = "seen"
	PresenceInApp     PresenceState = "in_app"
	PresenceElsewhere PresenceState = "elsewhere"
)

var PresenceStates = []PresenceState{PresenceSeen, PresenceInApp, PresenceElsewhere}

type PresenceGroup string

const (
	PresenceByThread    PresenceGroup = "thread"
	PresenceByTodo      PresenceGroup = "todo"
	PresenceByWorkspace PresenceGroup = "workspace"
	PresenceByDay       PresenceGroup = "day"
	PresenceByApp       PresenceGroup = "app"
)

var PresenceGroups = []PresenceGroup{PresenceByThread, PresenceByTodo, PresenceByWorkspace, PresenceByDay, PresenceByApp}

// PresenceQuery sums the time the user spent over a half-open range. From and
// To clip a stretch that crosses them, so the buckets of adjacent ranges add
// up. A day grouping splits a stretch at the days of the caller's time zone;
// an app grouping returns a single bucket keyed "".
type PresenceQuery struct {
	From, To time.Time
	Group    PresenceGroup
	// OffsetMinutes is how many minutes east of UTC the days of a day grouping
	// start, so a client sees the days of its own time zone.
	OffsetMinutes int
}

// PresenceBucket is a stretch of time per group key: a thread ID, a todo ID, a
// workspace ID, a day (YYYY-MM-DD in the caller's time zone) or "" for the
// app as a whole.
type PresenceBucket struct {
	Key     string
	Seconds int64
}

type Event struct {
	Seq    int64
	User   string
	Thread string
	Change *Change
	Owned  *Owned
	Delta  *Delta
}

type Owned struct {
	Settings           *account.Settings
	Workspace          *account.Workspace
	Environment        *account.Environment
	EnvironmentDeleted string
	MCPServer          *account.MCPServer
	MCPServerDeleted   string
	CronJob            *CronJobView
	CronJobDeleted     string
	Memory             *memory.Memory
	Todo               *account.Todo
}

type Change struct {
	Entries []conversation.Entry
	Items   []conversation.Item
	Turns   []conversation.Turn
	Header  *Thread
	Deleted bool
	Active  string
}

type Delta struct {
	Turn      string
	Item      string
	Text      string
	Reasoning string
}

// TodoFilter picks the todos to read: the given statuses, or all of them when none are given, and at most
// Limit of them, or all when Limit is 0.
type TodoFilter struct {
	Statuses []account.TodoStatus
	Limit    int
}

type ThreadFilter struct {
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
	CronJobs []CronJobView
}

type CronJobView struct {
	account.CronJob
	Title     string
	Workspace string
	Archived  bool
	LastTurn  string
	LastRun   *time.Time
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

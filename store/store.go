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
}

type Tx interface {
	Reader
	EnsureUser(id string) error
	SetSettings(user string, s account.Settings) error
	PutWorkspace(user string, w account.Workspace) error
	PutEnvironment(user string, e account.Environment) error
	DeleteEnvironment(user, id string) error
	PutMCPServer(user string, s account.MCPServer) error
	DeleteMCPServer(user, name string) error
	PutCronJob(j account.CronJob) error
	DeleteCronJob(id string) error
	PutMemory(m memory.Memory) error
	SetMemoryState(user string, s memory.State) error
	PutUsage(u Usage) error
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

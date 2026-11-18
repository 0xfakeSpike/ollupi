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
	Settings(user string) (user.Settings, error)
	Workspaces(user string) ([]user.Workspace, error)
	Workspace(user, id string) (user.Workspace, error)
	Environments(user string) ([]user.Environment, error)
	Environment(user, id string) (user.Environment, error)
	EnvironmentInUse(user, id string) (bool, error)
	WorkspaceBusy(user, id string) (bool, error)
	MCPServers(user string) ([]user.MCPServer, error)
	CronJobs(user, thread string) ([]CronJobView, error)
	CronJob(id string) (user.CronJob, error)
	Due(now time.Time) ([]user.CronJob, error)
	Threads(user string, f ThreadFilter) (threads []Thread, more bool, err error)
	Waiting(user string) ([]string, error)
	Children(thread string) ([]Thread, error)
	Search(user string, terms []string, workspace string, limit int) (Found, error)
	Busy() (int64, error)
	Active(since time.Time) (int64, error)

	Thread(id string) (Thread, error)
	View(thread string) (conversation.View, error)
	Page(thread string, before int64, limit int) (page Page, more bool, err error)
	Runnable(limit int, except []string) ([]Runnable, error)
	Seq() (int64, error)

	Memories(user string, places ...memory.Place) ([]memory.Memory, error)
	Memory(user, id string) (memory.Memory, error)
	MemoryState(user string) (memory.State, error)
	EndedTurns(user string, after memory.Cursor, limit int) ([]memory.Cursor, error)
	Unread(user string, after memory.Cursor) (int64, error)
}

type Tx interface {
	Reader
	EnsureUser(id string) error
	SetSettings(user string, s user.Settings) error
	PutWorkspace(user string, w user.Workspace) error
	PutEnvironment(user string, e user.Environment) error
	DeleteEnvironment(user, id string) error
	PutMCPServer(user string, s user.MCPServer) error
	DeleteMCPServer(user, name string) error
	PutCronJob(j user.CronJob) error
	DeleteCronJob(id string) error
	PutMemory(m memory.Memory) error
	SetMemoryState(user string, s memory.State) error
	DeleteThread(id string) error
	CreateThread(t Thread) error
	UpdateThread(t Thread) error
	Append(thread string, entries ...conversation.Entry) (conversation.View, error)
}

type Thread struct {
	user.Thread
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
	Thread string
	User   string
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
	Settings           *user.Settings
	Workspace          *user.Workspace
	Environment        *user.Environment
	EnvironmentDeleted string
	MCPServer          *user.MCPServer
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
	user.CronJob
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

// Package store is where a shard keeps what it knows: each conversation's log and the views folded from it, beside the
// headers of the conversations. Every write is a transaction; once it commits, what it changed reaches the feed. The
// package says what a store does; store/sqlite is one.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
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
	Reader(ctx context.Context) Reader
	Feed() *Feed
	// Fence writes epoch as the shard's, unless a later holder's is there, and from then on refuses this process's
	// writes once a later one is written.
	Fence(epoch int64) error
	Close() error
}

type Reader interface {
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
}

type Tx interface {
	Reader
	EnsureUser(id string) error
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

// Event is what reaches the feed: a commit's change to one thread, or a delta of what a model is writing now, which
// is not kept and has no seq.
type Event struct {
	Seq    int64
	User   string
	Thread string
	Change *Change
	Delta  *Delta
}

// Change is what one commit did to one thread: the entries appended to its log and the items and turns they changed,
// as they are now, its header when the commit wrote it, and its status.
type Change struct {
	Entries []conversation.Entry
	Items   []conversation.Item
	Turns   []conversation.Turn
	Header  *Thread
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

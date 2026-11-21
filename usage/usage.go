// Package usage derives the usage table from the conversation entries: one row
// per model response, written by a background consumer rather than on the path
// that appends the entry. The feed only wakes the consumer; the entries are the
// truth, so a missed wake-up or a restart is repaired by a scan.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
)

const (
	batchSize  = 256
	maxQueued  = 256
	retryAfter = 30 * time.Second
)

type Recorder struct {
	st store.Store

	mu      sync.Mutex
	dirty   map[string]bool
	stale   bool
	retryAt time.Time

	wake chan struct{}
}

func New(st store.Store) *Recorder {
	return &Recorder{st: st, dirty: map[string]bool{}, stale: true, wake: make(chan struct{}, 1)}
}

// Run records usage until the context is done. It never returns another error:
// the host starts it in an errgroup where any error cancels the whole engine,
// so a failed write is logged and retried instead.
func (r *Recorder) Run(ctx context.Context) error {
	sub := r.st.Feed().SubscribeAll(r.hear)
	defer sub.Close()
	for {
		r.drain(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var timer <-chan time.Time
		if due := r.retryAtTime(); !due.IsZero() {
			timer = time.After(time.Until(due))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
		case <-timer:
		}
	}
}

// hear runs on the goroutine that committed the entries and holds the feed's
// read lock, so it only remembers which threads to look at. Touching the store
// here would deadlock on the store's lock and hold up every other subscriber.
func (r *Recorder) hear(e store.Event) {
	if e.Change == nil {
		return
	}
	responded := false
	for _, entry := range e.Change.Entries {
		if _, ok := entry.Body.(conversation.ModelResponded); ok {
			responded = true
			break
		}
	}
	if !responded {
		return
	}
	r.mu.Lock()
	if len(r.dirty) < maxQueued {
		r.dirty[e.Thread] = true
	} else {
		// The queue is full: look at every thread on the next pass rather than
		// dropping this event's thread.
		r.stale = true
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Recorder) drain(ctx context.Context) {
	threads, stale := r.take()
	for _, thread := range threads {
		if ctx.Err() != nil {
			return
		}
		if err := r.fill(ctx, thread); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.failed(thread, err)
		}
	}
	if !stale {
		return
	}
	if err := r.fill(ctx, ""); err != nil {
		if ctx.Err() != nil {
			return
		}
		r.failed("", err)
	}
}

func (r *Recorder) take() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	threads := make([]string, 0, len(r.dirty))
	for thread := range r.dirty {
		threads = append(threads, thread)
	}
	clear(r.dirty)
	stale := r.stale
	r.stale = false
	r.retryAt = time.Time{}
	return threads, stale
}

func (r *Recorder) failed(thread string, err error) {
	slog.Warn("usage: a model response's tokens are not recorded yet; retrying", "thread", thread, "error", err)
	r.mu.Lock()
	defer r.mu.Unlock()
	if thread == "" || len(r.dirty) >= maxQueued {
		r.stale = true
	} else {
		r.dirty[thread] = true
	}
	r.retryAt = time.Now().Add(retryAfter)
}

func (r *Recorder) retryAtTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retryAt
}

// fill writes the usage rows of the threads whose entries have none, a page at
// a time. The position is a (thread, seq) key, never rowid: deleting the last
// row makes SQLite reuse a rowid, which would skip entries.
func (r *Recorder) fill(ctx context.Context, thread string) error {
	after := store.UsageKey{Thread: thread}
	for {
		var entries []store.UsageEntry
		var more bool
		err := r.st.Read(ctx, func(rd store.Reader) (err error) {
			entries, more, err = rd.MissingUsage(thread, after, batchSize)
			return err
		})
		if err != nil || len(entries) == 0 {
			return err
		}
		rows := make([]store.Usage, 0, len(entries))
		for _, e := range entries {
			var body conversation.ModelResponded
			if err := json.Unmarshal(e.Body, &body); err != nil {
				slog.Warn("usage: a model response cannot be read; skipping it", "thread", e.Thread, "seq", e.Seq, "error", err)
				continue
			}
			source := store.UsageResponse
			if body.Compaction {
				source = store.UsageCompaction
			}
			rows = append(rows, store.Usage{ID: usageID(e.Thread, e.Seq), Thread: e.Thread, Seq: e.Seq, User: e.User,
				Workspace: e.Workspace, Turn: e.Turn, Item: e.Item, At: e.At.UTC(), Source: source, Model: body.Stats.Model,
				Tokens: body.Usage})
		}
		if len(rows) > 0 {
			if err := r.st.Update(ctx, func(tx store.Tx) error {
				for _, row := range rows {
					if err := tx.PutUsage(row); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		last := entries[len(entries)-1]
		after = store.UsageKey{Thread: last.Thread, Seq: last.Seq}
		if !more {
			return nil
		}
	}
}

func usageID(thread string, seq int64) string {
	return fmt.Sprintf("%s:%d", thread, seq)
}

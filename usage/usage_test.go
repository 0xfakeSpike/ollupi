package usage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
)

type usageRow struct {
	ID                    string
	ThreadID              string
	Seq                   int64
	UserID                string
	WorkspaceID           string
	TurnID                string
	ItemID                string
	Source                string
	Model                 string
	InputTokens           int64
	CachedInputTokens     int64
	CacheWriteInputTokens int64
	OutputTokens          int64
	At                    time.Time
}

func open(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func update(t *testing.T, s store.Store, fn func(store.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func read[T any](t *testing.T, s store.Store, fn func(store.Reader) (T, error)) T {
	t.Helper()
	var out T
	if err := s.Read(context.Background(), func(r store.Reader) (err error) { out, err = fn(r); return err }); err != nil {
		t.Fatal(err)
	}
	return out
}

// converse starts a thread in workspace "w" and appends one model response per
// entry, all in a single commit.
func converse(t *testing.T, s store.Store, user, thread string, responses ...conversation.ModelResponded) {
	t.Helper()
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		if err := tx.EnsureUser(user); err != nil {
			return err
		}
		if err := tx.PutWorkspace(user, account.Workspace{ID: "w", Name: "proj", Root: t.TempDir(), Created: now, Updated: now}); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: user, Thread: account.Thread{ID: thread, Workspace: "w", Title: "chat", Created: now, Updated: now}}); err != nil {
			return err
		}
		entries := []conversation.Entry{{Turn: "t1", Body: conversation.TurnQueued{}},
			{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "hi"}}}
		for _, r := range responses {
			entries = append(entries, conversation.Entry{Turn: "t1", Body: r})
		}
		entries = append(entries, conversation.Entry{Turn: "t1", Item: "f1", Body: conversation.ModelFailed{Error: "no usage here"}},
			conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}})
		_, err := tx.Append(thread, entries...)
		return err
	})
}

func rows(t *testing.T, s *sqlite.Store) []usageRow {
	t.Helper()
	var out []usageRow
	if err := s.Gorm().Raw("SELECT * FROM usage ORDER BY thread_id, seq").Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func run(t *testing.T, r *Recorder) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return cancel, done
}

func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the recorder ended with %v", err)
	}
}

func TestTheRecorderWritesWhatTheResponsesSay(t *testing.T) {
	s := open(t)
	converse(t, s, "u", "th",
		conversation.ModelResponded{Response: "r1", Usage: conversation.Usage{InputTokens: 10, CachedInputTokens: 4,
			CacheWriteInputTokens: 2, OutputTokens: 3}, Stats: conversation.ContextStats{Model: "m1"}},
		conversation.ModelResponded{Response: "summary", Compaction: true,
			Usage: conversation.Usage{InputTokens: 5, OutputTokens: 1}, Stats: conversation.ContextStats{Model: "m1"}})
	r := New(s)
	cancel, done := run(t, r)
	waitFor(t, "the two responses", func() bool { return len(rows(t, s)) == 2 })
	got := rows(t, s)
	first, second := got[0], got[1]
	if first.Source != string(store.UsageResponse) || second.Source != string(store.UsageCompaction) {
		t.Errorf("sources %q and %q", first.Source, second.Source)
	}
	if first.ID != "th:3" || first.Seq != 3 || second.ID != "th:4" || second.Seq != 4 {
		t.Errorf("the rows' keys %q/%d and %q/%d", first.ID, first.Seq, second.ID, second.Seq)
	}
	if first.UserID != "u" || first.WorkspaceID != "w" || first.ThreadID != "th" || first.TurnID != "t1" || first.ItemID != "" {
		t.Errorf("the first row's owner %+v", first)
	}
	if first.Model != "m1" || first.InputTokens != 10 || first.CachedInputTokens != 4 || first.CacheWriteInputTokens != 2 || first.OutputTokens != 3 {
		t.Errorf("the first row's tokens %+v", first)
	}
	if first.At.IsZero() || second.At.IsZero() {
		t.Errorf("a row has no time: %+v", got)
	}
	// Nothing is left to fill: the entries without a response are not missing usage.
	var missing []store.UsageEntry
	if err := s.Read(context.Background(), func(rd store.Reader) (err error) {
		missing, _, err = rd.MissingUsage("", store.UsageKey{}, 10)
		return err
	}); err != nil || len(missing) != 0 {
		t.Errorf("still missing %d, %v", len(missing), err)
	}
	stop(t, cancel, done)
}

func TestTheRecorderHearsANewResponse(t *testing.T) {
	s := open(t)
	converse(t, s, "u", "th", conversation.ModelResponded{Response: "r1", Usage: conversation.Usage{InputTokens: 1}})
	r := New(s)
	cancel, done := run(t, r)
	waitFor(t, "the first response", func() bool { return len(rows(t, s)) == 1 })
	update(t, s, func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t2", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t2", Body: conversation.ModelResponded{Response: "r2",
				Usage: conversation.Usage{InputTokens: 7}, Stats: conversation.ContextStats{Model: "m2"}}})
		return err
	})
	waitFor(t, "the second response", func() bool { return len(rows(t, s)) == 2 })
	if got := rows(t, s)[1]; got.InputTokens != 7 || got.TurnID != "t2" || got.Model != "m2" {
		t.Errorf("the second row %+v", got)
	}
	stop(t, cancel, done)
}

func TestARestartFillsWhatWasMissed(t *testing.T) {
	s := open(t)
	var responses []conversation.ModelResponded
	for i := range 300 {
		responses = append(responses, conversation.ModelResponded{Response: fmt.Sprintf("r%d", i), Usage: conversation.Usage{InputTokens: int64(i + 1)}})
	}
	converse(t, s, "u", "th", responses...)
	if len(rows(t, s)) != 0 {
		t.Fatal("a row was written without the recorder")
	}
	cancel, done := run(t, New(s))
	waitFor(t, "every response", func() bool { return len(rows(t, s)) == 300 })
	stop(t, cancel, done)

	// Lose the newest row, as a crash between the entry and its usage row would,
	// and add another response. A scan keyed on rowid would skip the gap.
	gone := rows(t, s)[299]
	if err := s.Gorm().Exec("DELETE FROM usage WHERE id = ?", gone.ID).Error; err != nil {
		t.Fatal(err)
	}
	update(t, s, func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t2", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t2", Body: conversation.ModelResponded{Response: "later",
				Usage: conversation.Usage{InputTokens: 1000}}})
		return err
	})
	cancel, done = run(t, New(s))
	waitFor(t, "the lost and the new response", func() bool {
		got := rows(t, s)
		return len(got) == 301 && got[299].ID == gone.ID && got[300].InputTokens == 1000
	})
	stop(t, cancel, done)
}

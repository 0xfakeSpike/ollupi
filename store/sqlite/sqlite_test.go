package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
)

func open(t *testing.T, home string) *Store {
	t.Helper()
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func start(t *testing.T, s *Store, owner, thread string) {
	t.Helper()
	now := time.Now().UTC()
	err := s.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.EnsureUser(owner); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: owner, Thread: user.Thread{ID: thread, Title: "a chat", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append(thread,
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{Role: "full-main-agent"}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "hi"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnAppendIsKeptFoldedAndToldOnceCommitted(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	start(t, s, "u", "th")
	if len(heard) != 1 || heard[0].Seq != 2 || heard[0].Change.Header == nil || len(heard[0].Change.Entries) != 2 || !heard[0].Change.Runnable {
		t.Fatalf("heard %+v", heard)
	}
	v, err := s.Reader(ctx).View("th")
	if err != nil || v.Head != 2 || !v.Runnable() || len(v.Items) != 1 || v.Items[0].Text != "hi" {
		t.Fatalf("view %+v, %v", v, err)
	}
	if run, err := s.Reader(ctx).Runnable(10, nil); err != nil || len(run) != 1 || run[0].Thread != "th" || run[0].User != "u" {
		t.Errorf("runnable %+v, %v", run, err)
	}

	err = s.Update(ctx, func(tx store.Tx) error {
		if _, err := tx.Append("th", conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolCall{Call: "x", Tool: "run_command", Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ApprovalAsked{Approval: "a1"}}); err != nil {
			return err
		}
		_, err := tx.Append("th", conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolStarted{}})
		return err
	})
	if !errors.Is(err, conversation.ErrRefused) {
		t.Fatalf("a call started while it waits for approval: %v", err)
	}
	if len(heard) != 1 {
		t.Error("a transaction that failed was told")
	}
	if v, _ := s.Reader(ctx).View("th"); v.Head != 2 {
		t.Errorf("a transaction that failed was kept: head %d", v.Head)
	}

	err = s.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolCall{Call: "x", Tool: "run_command", Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ApprovalAsked{Approval: "a1"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	last := heard[len(heard)-1]
	if last.Seq != 5 || last.Change.Status != conversation.ThreadWaitingOnApproval || last.Change.Runnable || len(last.Change.Items) != 1 || last.Change.Items[0].Approval == nil {
		t.Fatalf("the approval's change: %+v", last.Change)
	}
	if run, _ := s.Reader(ctx).Runnable(10, nil); len(run) != 0 {
		t.Errorf("a thread that waits is runnable: %+v", run)
	}
	if th, err := s.Reader(ctx).Thread("th"); err != nil || th.Status != conversation.ThreadWaitingOnApproval || th.Head != 5 {
		t.Errorf("the thread %+v, %v", th, err)
	}
}

func TestATurnEndedMarksTheThreadRepliedAndThePageReadsBackwards(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	start(t, s, "u", "th")
	err := s.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "a1", Body: conversation.AgentMessage{Text: "hello", Response: "r1"}},
			conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}},
			conversation.Entry{Turn: "t2", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t2", Item: "m2", Body: conversation.UserMessage{Text: "again"}, At: time.Unix(1, 0).UTC()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Reader(ctx)
	if th, _ := r.Thread("th"); th.Replied == nil {
		t.Error("a turn ended leaves the thread unreplied")
	}
	page, more, err := r.Page("th", 0, 2)
	if err != nil || !more || len(page.Items) != 2 || page.Items[0].ID != "a1" || page.Items[1].ID != "m2" || len(page.Turns) != 2 {
		t.Fatalf("the latest page %+v, more %v, %v", page, more, err)
	}
	older, more, err := r.Page("th", page.Items[0].Seq, 2)
	if err != nil || more || len(older.Items) != 1 || older.Items[0].ID != "m1" {
		t.Errorf("the page before %+v, more %v, %v", older, more, err)
	}
	if seq, err := r.Seq(); err != nil || seq != 7 {
		t.Errorf("seq %d, %v", seq, err)
	}
}

func TestALaterHolderFencesTheOneBefore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	first := open(t, home)
	if err := first.Fence(1); err != nil {
		t.Fatal(err)
	}
	start(t, first, "u", "th")
	second := open(t, home)
	if err := second.Fence(2); err != nil {
		t.Fatal(err)
	}
	err := first.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser("late") })
	if !errors.Is(err, store.ErrShardLost) {
		t.Errorf("the earlier holder's write: %v", err)
	}
	if err := first.Fence(1); !errors.Is(err, store.ErrShardLost) {
		t.Errorf("fencing again under the earlier epoch: %v", err)
	}
	if err := second.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser("taker") }); err != nil {
		t.Errorf("the later holder's write: %v", err)
	}
}

func TestAReopenedStoreReadsTheSameView(t *testing.T) {
	home := t.TempDir()
	s := open(t, home)
	start(t, s, "u", "th")
	out, _ := json.Marshal(map[string]string{"files": "a.go"})
	err := s.Update(context.Background(), func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r1", Usage: conversation.Usage{InputTokens: 9}}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolCall{Call: "x", Tool: "read_file", Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolStarted{}},
			conversation.Entry{Turn: "t1", Item: "c1", Body: conversation.ToolResult{Output: out}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	again := open(t, home)
	v, err := again.Reader(context.Background()).View("th")
	if err != nil || v.Head != 6 || len(v.Responses) != 1 || v.Turns[0].Usage.InputTokens != 9 {
		t.Fatalf("view %+v, %v", v, err)
	}
	if c, _ := v.ItemOf("c1"); c.Status != conversation.Done || string(c.Result.Output) != string(out) {
		t.Errorf("the call %+v", c)
	}
}

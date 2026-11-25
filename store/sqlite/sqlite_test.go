package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/store"
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
		if err := tx.EnsureUser(owner, account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		scratch, err := scratchOf(tx, owner)
		if err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: owner, Thread: account.Thread{ID: thread, Workspace: scratch, Title: "a chat", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err = tx.Append(thread,
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
	if len(heard) != 3 || heard[0].Seq != 1 || heard[0].Change == nil || heard[0].Change.Header == nil || len(heard[0].Change.Entries) != 2 || !heard[0].Change.Header.Runnable ||
		heard[1].Owned == nil || heard[1].Owned.Environment == nil || heard[2].Owned == nil || heard[2].Owned.Workspace == nil || !heard[2].Owned.Workspace.Scratch() {
		t.Fatalf("heard %+v", heard)
	}
	v := view(t, s, "th")
	if v.Head != 2 || !v.Runnable() || len(v.Items) != 1 || v.Items[0].Text != "hi" {
		t.Fatalf("view %+v", v)
	}
	if run := runnable(t, s); len(run) != 1 || run[0].Thread != "th" || run[0].User != "u" {
		t.Errorf("runnable %+v", run)
	}

	err := s.Update(ctx, func(tx store.Tx) error {
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
	if len(heard) != 3 {
		t.Error("a transaction that failed was told")
	}
	if v := view(t, s, "th"); v.Head != 2 {
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
	if last.Seq != 2 || last.Change.Header.Status != conversation.ThreadWaitingOnApproval || last.Change.Header.Runnable || len(last.Change.Items) != 1 || last.Change.Items[0].Approval == nil {
		t.Fatalf("the approval's change: %+v", last.Change)
	}
	if run := runnable(t, s); len(run) != 0 {
		t.Errorf("a thread that waits is runnable: %+v", run)
	}
	if th := thread(t, s, "th"); th.Status != conversation.ThreadWaitingOnApproval || th.Head != 5 {
		t.Errorf("the thread %+v", th)
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
	if th := thread(t, s, "th"); th.Replied == nil {
		t.Error("a turn ended leaves the thread unreplied")
	}
	var page, older store.Page
	var more, olderMore bool
	var seq int64
	err = s.Read(ctx, func(r store.Reader) error {
		var err error
		if page, more, err = r.Page("th", 0, 2); err != nil {
			return err
		}
		if older, olderMore, err = r.Page("th", page.Items[0].Seq, 2); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	if err != nil || !more || len(page.Items) != 2 || page.Items[0].ID != "a1" || page.Items[1].ID != "m2" || len(page.Turns) != 2 {
		t.Fatalf("the latest page %+v, more %v, %v", page, more, err)
	}
	if olderMore || len(older.Items) != 1 || older.Items[0].ID != "m1" {
		t.Errorf("the page before %+v, more %v", older, olderMore)
	}
	if seq != 2 {
		t.Errorf("seq %d after two commits", seq)
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
	err := first.Update(ctx, func(tx store.Tx) error {
		return tx.EnsureUser("late", account.NewEnvironment("This computer", "local", nil))
	})
	if !errors.Is(err, store.ErrShardLost) {
		t.Errorf("the earlier holder's write: %v", err)
	}
	if err := first.Fence(1); !errors.Is(err, store.ErrShardLost) {
		t.Errorf("fencing again under the earlier epoch: %v", err)
	}
	if err := second.Update(ctx, func(tx store.Tx) error {
		return tx.EnsureUser("taker", account.NewEnvironment("This computer", "local", nil))
	}); err != nil {
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
	v := view(t, again, "th")
	if v.Head != 6 || len(v.Responses) != 1 || v.Turns[0].Usage.InputTokens != 9 {
		t.Fatalf("view %+v", v)
	}
	if c, _ := v.ItemOf("c1"); c.Status != conversation.Done || string(c.Result.Output) != string(out) {
		t.Errorf("the call %+v", c)
	}
}

func TestDeletingAUserRemovesTheirConversationData(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now().UTC()
	err := s.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("gone", account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		scratch, err := scratchOf(tx, "gone")
		if err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "gone", Thread: account.Thread{ID: "th", Workspace: scratch, Title: "x", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err = tx.Append("th",
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "hi"}},
			conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}})
		if err != nil {
			return err
		}
		if _, err := tx.AddTodo("gone", account.Todo{Title: "x", Status: account.TodoOpen, Thread: "th", Created: now, Updated: now}); err != nil {
			return err
		}
		return tx.PutMemory(memory.Memory{ID: "mem", User: "gone", Place: memory.GlobalPlace, Origin: scratch, Topic: "t", Kind: memory.Fact, Text: "x", Status: memory.Active, Created: now, Updated: now})
	})
	if err != nil {
		t.Fatal(err)
	}
	var threads []string
	if err := s.Update(ctx, func(tx store.Tx) (err error) { threads, err = tx.DeleteUser("gone"); return err }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(threads, []string{"th"}) {
		t.Fatalf("threads = %v", threads)
	}
	var users []string
	if err := s.Read(ctx, func(r store.Reader) (err error) { users, err = r.Users(); return err }); err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("users = %v", users)
	}
	var left int64
	if err := s.g.Raw("SELECT (SELECT count(*) FROM memories) + (SELECT count(*) FROM todos) + (SELECT count(*) FROM entries)").Scan(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d rows of the deleted user are left", left)
	}
}

func view(t *testing.T, s *Store, thread string) conversation.View {
	t.Helper()
	var v conversation.View
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		v, err = r.View(thread)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

func thread(t *testing.T, s *Store, id string) store.Thread {
	t.Helper()
	var th store.Thread
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		th, err = r.Thread(id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return th
}

func runnable(t *testing.T, s *Store) []store.Runnable {
	t.Helper()
	var out []store.Runnable
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, err = r.Runnable(10, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestASubagentWaitingOnTheUserMakesItsParentWait(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "th")
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		scratch, err := scratchOf(tx, "u")
		if err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: "child", Workspace: scratch, Parent: "th", ParentItem: "launch", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err = tx.Append("child", conversation.Entry{Turn: "c1", Body: conversation.TurnQueued{Role: "general-sub-agent"}},
			conversation.Entry{Turn: "c1", Item: "m1", Body: conversation.UserMessage{Text: "look"}})
		return err
	})
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	update(t, s, func(tx store.Tx) error {
		_, err := tx.Append("child", conversation.Entry{Turn: "c1", Body: conversation.ModelResponded{Response: "r1"}},
			conversation.Entry{Turn: "c1", Item: "c", Body: conversation.ToolCall{Call: "x", Tool: "run_command", Response: "r1"}},
			conversation.Entry{Turn: "c1", Item: "c", Body: conversation.ApprovalAsked{Approval: "a1"}})
		return err
	})
	told := slices.ContainsFunc(heard, func(e store.Event) bool {
		return e.Thread == "th" && e.Change != nil && e.Change.Header.Status == conversation.ThreadWaitingOnApproval
	})
	if th := thread(t, s, "th"); th.Status != conversation.ThreadWaitingOnApproval || !told {
		t.Errorf("the parent %s, told %v", th.Status, told)
	}
	waiting := read(t, s, func(r store.Reader) ([]string, error) { return r.Waiting("u") })
	if len(waiting) != 1 || waiting[0] != "child" {
		t.Errorf("waiting on the user themselves %v", waiting)
	}
	update(t, s, func(tx store.Tx) error {
		_, err := tx.Append("child", conversation.Entry{Turn: "c1", Item: "c", Body: conversation.Decided{Decision: conversation.Decline, By: "u"}})
		return err
	})
	if th := thread(t, s, "th"); th.Status != conversation.ThreadActive {
		t.Errorf("the parent once answered %s", th.Status)
	}
}

// scratchOf is the scratch workspace of the user's first environment, where
// a conversation started in no workspace is.
func scratchOf(r store.Reader, user string) (string, error) {
	envs, err := r.Environments(user)
	if err != nil {
		return "", err
	}
	w, err := r.Scratch(user, envs[0].ID)
	return w.ID, err
}

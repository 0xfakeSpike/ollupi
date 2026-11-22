package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestTodosComeBackUnfinishedThenFinishedNewestFirst(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "chat")
	day := func(d int) *time.Time {
		at := time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC)
		return &at
	}
	add := func(title string, st account.TodoStatus, ended *time.Time, thread string) account.Todo {
		var added account.Todo
		now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		update(t, s, func(tx store.Tx) error {
			var err error
			added, err = tx.AddTodo("u", account.Todo{Title: title, Status: st, Ended: ended, Thread: thread, Created: now, Updated: now})
			return err
		})
		return added
	}
	first := add("first", account.TodoOpen, nil, "")
	doneEarly := add("done early", account.TodoDone, day(1), "")
	doneLate := add("done late", account.TodoDone, day(9), "")
	handed := add("handed", account.TodoHanded, nil, "chat")

	if first.ID != 1 || handed.ID != 4 {
		t.Errorf("the ids %d and %d", first.ID, handed.ID)
	}
	sortOf := func(todos []account.Todo) []int64 {
		var ids []int64
		for _, td := range todos {
			ids = append(ids, td.ID)
		}
		return ids
	}
	all, more := read2(t, s, func(r store.Reader) ([]account.Todo, bool, error) { return r.Todos("u", store.TodoFilter{}) })
	if more || len(all) != 4 {
		t.Fatalf("read %+v, more %v", all, more)
	}
	if ids := sortOf(all); len(ids) != 4 || ids[0] != first.ID || ids[1] != handed.ID || ids[2] != doneLate.ID || ids[3] != doneEarly.ID {
		t.Errorf("the order %v", ids)
	}

	limited, moreAgain := read2(t, s, func(r store.Reader) ([]account.Todo, bool, error) { return r.Todos("u", store.TodoFilter{Limit: 2}) })
	if !moreAgain || len(limited) != 2 {
		t.Errorf("a page of two: %+v, more %v", limited, moreAgain)
	}
	done, _ := read2(t, s, func(r store.Reader) ([]account.Todo, bool, error) {
		return r.Todos("u", store.TodoFilter{Statuses: []account.TodoStatus{account.TodoDone}})
	})
	if len(done) != 2 {
		t.Errorf("the finished ones %+v", done)
	}

	update(t, s, func(tx store.Tx) error { return tx.DeleteThread("chat") })
	after := read(t, s, func(r store.Reader) (account.Todo, error) { return r.Todo("u", handed.ID) })
	if after.Status != account.TodoOpen || after.Thread != "" {
		t.Errorf("a todo of the deleted conversation %+v", after)
	}
	if err := s.Read(context.Background(), func(r store.Reader) error {
		_, err := r.Todo("u", 99)
		return err
	}); err == nil {
		t.Error("a todo that is not there")
	}
}

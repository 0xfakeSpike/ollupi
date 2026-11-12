package repo

import (
	"fmt"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func TestThreadPagesFollowListOrder(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	// Two threads share an update time and a creation time, so only the id orders them.
	for i, updated := range []time.Duration{3, 2, 2, 2, 1} {
		created := at
		if i == 3 {
			created = at.Add(-time.Hour)
		}
		row := ThreadRow{UserID: DefaultUser, ID: fmt.Sprintf("t%d", i), CreatedAt: created, UpdatedAt: at.Add(updated * time.Minute)}
		if err := db.CreateThread(row); err != nil {
			t.Fatal(err)
		}
	}
	var listed []string
	var after *ThreadCursor
	for {
		page, _, err := db.ThreadRows(DefaultUser, ThreadFilter{After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			listed = append(listed, r.ID)
		}
		last := page[len(page)-1]
		after = &ThreadCursor{UpdatedAt: last.UpdatedAt, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if got, want := fmt.Sprint(listed), "[t0 t2 t1 t3 t4]"; got != want {
		t.Errorf("pages listed %s, want %s", got, want)
	}
}

// A thread is unread from the moment one of its turns ends until the user marks it read, and again after the next
// turn ends.
func TestAThreadIsUnreadFromATurnsEndUntilMarkedRead(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	unread := func(want bool) {
		t.Helper()
		row, err := db.ThreadRow("t")
		if err != nil {
			t.Fatal(err)
		}
		if row.Unread() != want {
			t.Fatalf("unread %v (replied %v, read %v), want %v", row.Unread(), row.RepliedAt, row.ReadAt, want)
		}
	}
	turn := func(id string) {
		t.Helper()
		_, started, _, err := db.SubmitTurn("t", TurnRow{ID: id, Input: "hi", Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		started.Status = TurnCompleted
		ended, _, _, err := db.EndTurn("t", started)
		if err != nil {
			t.Fatal(err)
		}
		if !ended.Unread() {
			t.Fatalf("the thread EndTurn returns is read; want it unread")
		}
	}
	unread(false)
	turn("one")
	unread(true)
	if row, err := db.MarkThreadRead("t", time.Now()); err != nil || row.Unread() {
		t.Fatalf("marked read: unread %v, %v", row.Unread(), err)
	}
	unread(false)
	time.Sleep(2 * time.Millisecond)
	turn("two")
	unread(true)
}

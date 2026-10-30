package repo

import (
	"fmt"
	"testing"
	"time"
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
		row := ThreadRow{ID: fmt.Sprintf("t%d", i), CreatedAt: created, UpdatedAt: at.Add(updated * time.Minute)}
		if err := db.CreateThread(row); err != nil {
			t.Fatal(err)
		}
	}
	var listed []string
	var after *ThreadCursor
	for {
		page, err := db.ThreadRows(ThreadFilter{After: after, Limit: 2})
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

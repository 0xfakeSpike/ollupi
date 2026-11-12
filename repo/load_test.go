package repo

import (
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func TestTheCountsALoadIsMeasuredBy(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	weekAgo := now.Add(-7 * 24 * time.Hour)
	for user, moved := range map[string]time.Time{"recent": now.Add(-time.Hour), "lapsed": weekAgo.Add(-time.Hour), "idle": {}} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
		if moved.IsZero() {
			continue
		}
		if err := db.CreateThread(ThreadRow{UserID: user, ID: user, CreatedAt: moved, UpdatedAt: moved}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := db.ActiveUsers(weekAgo); err != nil || n != 1 {
		t.Errorf("active users in the last week: %d, %v", n, err)
	}

	if n, err := db.TurnsUnderWay(); err != nil || n != 0 {
		t.Errorf("turns under way before any: %d, %v", n, err)
	}
	for _, thread := range []string{"recent", "lapsed"} {
		if _, _, _, err := db.SubmitTurn(thread, TurnRow{ID: thread, Input: "hi", Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := db.TurnsUnderWay(); err != nil || n != 2 {
		t.Errorf("turns under way: %d, %v", n, err)
	}
	if n, err := db.ActiveUsers(weekAgo); err != nil || n != 2 {
		t.Errorf("active users once lapsed submits a turn: %d, %v", n, err)
	}
}

package gateway

import (
	"errors"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

// The gateway reads some views straight from the database; those keep to the connection's user too.
func TestAConnectionReadsOnlyItsUsersRows(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(repo.ThreadRow{ID: "t", UserID: "a", Title: "mine", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	jobs := cron.New(db, time.UTC, func(string, string) {})
	s, err := NewServer(nil, Options{DB: db, Token: "secret", Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{s: s, user: "b"}

	if _, _, err := threadPage(false)(c, ThreadPageParams{ThreadID: "t"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("read the thread: %v", err)
	}
	if _, _, err := c.cronCreate(CronCreateParams{ThreadID: "t", Name: "x", Spec: "@daily", Prompt: "go"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("schedule a task: %v", err)
	}
	paused := true
	if _, _, err := c.cronPause(CronPauseParams{ThreadID: "t", Name: "x", Paused: &paused}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("pause a task: %v", err)
	}
	if _, _, err := c.cronDelete(CronDeleteParams{ThreadID: "t", Name: "x"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("delete a task: %v", err)
	}
	owner := &conn{s: s, user: "a"}
	if _, _, err := owner.cronCreate(CronCreateParams{ThreadID: "t", Name: "x", Spec: "@daily", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	list, after, err := c.cronList(CronListParams{})
	if err != nil {
		t.Fatal(err)
	}
	after()
	if len(list.Data) != 0 {
		t.Errorf("b lists tasks: %+v", list.Data)
	}
}

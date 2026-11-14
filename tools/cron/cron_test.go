package cron

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func testJobs(t *testing.T) (*Jobs, *db.DB) {
	t.Helper()
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Now().UTC()
	if err := store.CreateThread(db.ThreadRow{UserID: db.DefaultUser, ID: "t", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return New(store, time.Local, nil), store
}

// due makes the job's next run a minute ago.
func due(t *testing.T, store *db.DB, id string) {
	t.Helper()
	job, err := store.CronJobView(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCronJobNextRun(id, job.NextRunAt, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestATaskEndsAfterItsTimes(t *testing.T) {
	jobs, store := testJobs(t)
	two := 2
	job, err := jobs.Create("t", "twice", "@daily", "check", &two)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	send := func(string, string, string) error { sent++; return nil }

	due(t, store, job.ID)
	jobs.sendDue(context.Background(), send)
	view, err := store.CronJobView(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Runs != 1 || !view.NextRunAt.After(time.Now()) {
		t.Fatalf("after one run: runs %d, next %s", view.Runs, view.NextRunAt)
	}

	due(t, store, job.ID)
	jobs.sendDue(context.Background(), send)
	if _, err := store.CronJobView(job.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("after its last run the task is %v, want deleted", err)
	}
	if sent != 2 {
		t.Fatalf("sent %d times, want 2", sent)
	}
}

func TestATaskWithoutTimesRepeats(t *testing.T) {
	jobs, store := testJobs(t)
	job, err := jobs.Create("t", "daily", "@daily", "check", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		due(t, store, job.ID)
		jobs.sendDue(context.Background(), func(string, string, string) error { return nil })
	}
	view, err := store.CronJobView(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Runs != 3 {
		t.Fatalf("runs %d, want 3", view.Runs)
	}
}

func TestALastRunWhoseDateHasPassedIsRefused(t *testing.T) {
	jobs, _ := testJobs(t)
	one := 1
	past := time.Now().In(time.Local).Add(-2 * time.Hour)
	spec := past.Format("4 15 2 1 *")
	if _, err := jobs.Create("t", "gone", spec, "check", &one); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("a once task at %q: %v, want ErrInvalidJob", spec, err)
	}
	if _, err := jobs.Create("t", "yearly", spec, "check", nil); err != nil {
		t.Fatalf("the same spec repeating: %v", err)
	}
	ahead := time.Now().In(time.Local).Add(2 * time.Hour).Format("4 15 2 1 *")
	if _, err := jobs.Create("t", "soon", ahead, "check", &one); err != nil {
		t.Fatalf("a once task at %q: %v", ahead, err)
	}
	zero := 0
	if _, err := jobs.Create("t", "never", ahead, "check", &zero); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("times 0: %v, want ErrInvalidJob", err)
	}
}

func TestACrashInATickLeavesTheNextOne(t *testing.T) {
	jobs, store := testJobs(t)
	row, err := jobs.Create("t", "crashes", "@daily", "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	due(t, store, row.ID)
	jobs.sendDue(context.Background(), func(string, string, string) error { panic("a crash") })
}

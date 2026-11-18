package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
)

var shanghai, _ = time.LoadLocation("Asia/Shanghai")

func TestATaskIsCheckedAndRunsAtItsTimes(t *testing.T) {
	cal := NewCalendar(shanghai)
	now := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)
	for _, c := range []struct{ name, spec, prompt, err string }{
		{"Bad Name", "0 9 * * *", "p", "name"},
		{"daily", "0 9 * *", "p", "5 fields"},
		{"daily", "TZ=UTC 0 9 * * *", "p", "time-zone"},
		{"daily", "@every 5m", "p", "descriptor"},
		{"daily", "0 9 * * *", " ", "prompt"},
	} {
		if _, err := cal.Job("j", "th", Draft{Name: c.name, Spec: c.spec, Prompt: c.prompt, Times: nil}, now); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	once := 1
	if _, err := cal.Job("j", "th", Draft{Name: "past", Spec: "0 8 1 3 *", Prompt: "p", Times: &once}, now); err == nil || !strings.Contains(err.Error(), "next year") {
		t.Errorf("a one-time task whose time has passed: %v", err)
	}
	twice := 2
	job, err := cal.Job("j", "th", Draft{Name: "daily", Spec: "0 9 * * *", Prompt: "say hi", Times: &twice}, now)
	if err != nil || !job.NextRun.Equal(time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("the first run: %v %v", job.NextRun, err)
	}
	job, done, err := cal.Ran(job, job.NextRun, true)
	if err != nil || done || job.Runs != 1 || !job.NextRun.Equal(time.Date(2026, 3, 2, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("after a run: %+v %v %v", job, done, err)
	}
	if _, done, _ := cal.Ran(job, job.NextRun, true); !done {
		t.Error("a task run as many times as it was to is not done")
	}
	paused, _ := cal.Pause(job, true, now)
	if resumed, err := cal.Pause(paused, false, time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC)); err != nil || resumed.Paused ||
		!resumed.NextRun.Equal(time.Date(2026, 3, 6, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("resumed: %+v %v", resumed, err)
	}
}

type fixture struct {
	t   *testing.T
	st  store.Store
	cal Calendar
	job user.CronJob
}

func setup(t *testing.T) *fixture {
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, st: st, cal: NewCalendar(time.UTC)}
	now := time.Now().UTC()
	f.job, err = f.cal.Job("j", "th", Draft{Name: "hourly", Spec: "@hourly", Prompt: "check the news", Times: nil}, now)
	if err != nil {
		t.Fatal(err)
	}
	f.update(func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: user.Thread{ID: "th", Created: now, Updated: now}}); err != nil {
			return err
		}
		return tx.PutCronJob(f.job)
	})
	return f
}

func (f *fixture) update(fn func(store.Tx) error) {
	f.t.Helper()
	if err := f.st.Update(context.Background(), fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) stored() user.CronJob {
	f.t.Helper()
	var j user.CronJob
	if err := f.st.Read(context.Background(), func(r store.Reader) (err error) { j, err = r.CronJob("j"); return err }); err != nil {
		f.t.Fatal(err)
	}
	return j
}

func queue(tx store.Tx, thread, prompt, job string) error {
	turn := uuid.NewString()
	_, err := tx.Append(thread, conversation.Entry{Turn: turn, Body: conversation.TurnQueued{CronJob: job}},
		conversation.Entry{Turn: turn, Item: uuid.NewString(), Body: conversation.UserMessage{Text: prompt}})
	return err
}

func TestADueTaskIsSentAndARunStillWaitingIsSkipped(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	at := f.job.NextRun
	if err := sendDue(ctx, f.st, f.cal, queue, at.Add(-time.Second)); err != nil || f.stored().Runs != 0 {
		t.Fatalf("sent before it was due: %v", err)
	}
	if err := sendDue(ctx, f.st, f.cal, queue, at); err != nil {
		t.Fatal(err)
	}
	if j := f.stored(); j.Runs != 1 || !j.NextRun.Equal(at.Add(time.Hour)) {
		t.Fatalf("after the run: %+v", j)
	}
	if err := sendDue(ctx, f.st, f.cal, queue, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var v conversation.View
	f.st.Read(ctx, func(r store.Reader) (err error) { v, err = r.View("th"); return err })
	if len(v.Turns) != 2 {
		t.Fatalf("the first run is in progress, so the second waits behind it: %d turns", len(v.Turns))
	}
	var listed []store.Task
	f.st.Read(ctx, func(r store.Reader) (err error) { listed, err = r.CronJobs("u", ""); return err })
	if len(listed) != 1 || listed[0].LastTurn != v.Turns[1].ID || listed[0].LastRun == nil || !listed[0].LastRun.Equal(v.Turns[1].QueuedAt) {
		t.Errorf("the task's last run %+v", listed)
	}
	if err := sendDue(ctx, f.st, f.cal, queue, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if j := f.stored(); j.Runs != 2 || !j.NextRun.Equal(at.Add(3*time.Hour)) {
		t.Errorf("a run while the one before still waits is skipped: %+v", j)
	}
}

func TestARunTheConversationRefusesIsSkipped(t *testing.T) {
	f := setup(t)
	refuse := func(store.Tx, string, string, string) error { return errors.New("the workspace is archived") }
	if err := sendDue(context.Background(), f.st, f.cal, refuse, f.job.NextRun); err != nil {
		t.Fatal(err)
	}
	if j := f.stored(); j.Runs != 0 || !j.NextRun.Equal(f.job.NextRun.Add(time.Hour)) {
		t.Errorf("the refused run: %+v", j)
	}
}

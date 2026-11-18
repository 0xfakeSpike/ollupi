package schedule

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
)

type runner func(ctx context.Context, thread string) error

func (r runner) Run(ctx context.Context, thread string) error { return r(ctx, thread) }

func open(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ask(t *testing.T, st store.Store, thread string) {
	t.Helper()
	now := time.Now().UTC()
	err := st.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: thread, Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append(thread, conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "hi"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func end(st store.Store, thread string, status conversation.End, reason string) error {
	return st.Update(context.Background(), func(tx store.Tx) error { return run.End(tx, thread, status, reason) })
}

func start(t *testing.T, s *Scheduler) func() error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came")
	}
	panic("unreachable")
}

func ended(t *testing.T, st store.Store, thread string) *conversation.Ending {
	t.Helper()
	var v conversation.View
	if err := st.Read(context.Background(), func(r store.Reader) (err error) { v, err = r.View(thread); return err }); err != nil {
		t.Fatal(err)
	}
	return v.Turns[0].Ended
}

func TestConversationsRunInTheSlotsThereAre(t *testing.T) {
	st := open(t)
	started := make(chan string, 10)
	release := make(chan struct{})
	var mu sync.Mutex
	running, most := 0, 0
	s := New(st, runner(func(ctx context.Context, thread string) error {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		started <- thread
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return end(st, thread, conversation.Completed, "")
	}), 2, 2, time.Minute)
	stop := start(t, s)
	for i := range 3 {
		ask(t, st, fmt.Sprint("th", i))
	}
	receive(t, started)
	receive(t, started)
	select {
	case th := <-started:
		t.Fatalf("%s ran beyond the slots", th)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	receive(t, started)
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Errorf("stopped with %v", err)
	}
	if most != 2 {
		t.Errorf("at most %d ran at once", most)
	}
}

func TestATurnEndedElsewhereStopsItsRun(t *testing.T) {
	st := open(t)
	started, stopped := make(chan struct{}, 1), make(chan error, 1)
	s := New(st, runner(func(ctx context.Context, thread string) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- ctx.Err()
		return ctx.Err()
	}), 4, 4, time.Minute)
	stop := start(t, s)
	defer stop()
	ask(t, st, "th")
	receive(t, started)
	if err := end(st, "th", conversation.Interrupted, "interrupted by the user"); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, stopped); !errors.Is(err, context.Canceled) {
		t.Errorf("the run stopped with %v", err)
	}
	if e := ended(t, st, "th"); e == nil || e.Status != conversation.Interrupted {
		t.Errorf("the turn %+v", e)
	}
}

func TestARunThatFailsOrOverrunsEndsItsTurn(t *testing.T) {
	st := open(t)
	finished := make(chan string, 2)
	s := New(st, runner(func(ctx context.Context, thread string) error {
		defer func() { finished <- thread }()
		if thread == "slow" {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("the model is gone")
	}), 4, 4, 200*time.Millisecond)
	stop := start(t, s)
	defer stop()
	ask(t, st, "broken")
	ask(t, st, "slow")
	deadline := time.Now().Add(5 * time.Second)
	for ended(t, st, "broken") == nil || ended(t, st, "slow") == nil {
		if time.Now().After(deadline) {
			t.Fatal("the turns did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := ended(t, st, "broken"); e.Status != conversation.Failed || e.Reason != "the model is gone" {
		t.Errorf("the failed turn %+v", e)
	}
	if e := ended(t, st, "slow"); e.Status != conversation.Failed || e.Reason != "the turn reached its time limit of 200ms" {
		t.Errorf("the slow turn %+v", e)
	}
}

// Package schedule gives runnable conversations a slot to run in. It learns of them from the store's feed alone:
// whoever makes a conversation runnable, a message, a decision, a subagent's report or launch, only writes the store.
// A turn ended by someone else, as when the user interrupts it, stops its run the same way.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/store"
)

// Runner runs a conversation's turn until it waits or ends; an error is the turn's failure.
type Runner interface {
	Run(ctx context.Context, thread string) error
}

type Scheduler struct {
	st        store.Store
	runner    Runner
	slots     int
	userSlots int
	limit     time.Duration

	wake   chan struct{}
	failed chan error
	mu     sync.Mutex
	runs   map[string]*worker
	ofUser map[string]int
	done   sync.WaitGroup
}

type worker struct {
	user   string
	cancel context.CancelFunc
	// stopped says the turn was ended or the conversation deleted while it ran.
	stopped bool
}

// New runs at most slots turns at a time, and userSlots of one user's, each for at most limit.
func New(st store.Store, runner Runner, slots, userSlots int, limit time.Duration) *Scheduler {
	return &Scheduler{st: st, runner: runner, slots: slots, userSlots: userSlots, limit: limit,
		wake: make(chan struct{}, 1), failed: make(chan error, 1), runs: map[string]*worker{}, ofUser: map[string]int{}}
}

// Run schedules until ctx ends, or until the store fails. A turn running when it stops is left as it is, and goes on
// when a scheduler runs it again.
func (s *Scheduler) Run(ctx context.Context) error {
	sub := s.st.Feed().SubscribeAll(s.hear)
	defer sub.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.done.Wait()
	}()
	s.signal()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.failed:
			return err
		case <-s.wake:
			if err := s.admit(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) hear(e store.Event) {
	if e.Change == nil {
		return
	}
	if e.Change.Runnable {
		s.signal()
	}
	ended := slices.ContainsFunc(e.Change.Entries, func(entry conversation.Entry) bool {
		_, ok := entry.Body.(conversation.TurnEnded)
		return ok
	})
	if !ended && !e.Change.Deleted {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, running := s.runs[e.Thread]; running {
		w.stopped = true
		w.cancel()
	}
}

// admit starts the conversations runnable the longest, while slots are free.
func (s *Scheduler) admit(ctx context.Context) error {
	s.mu.Lock()
	free := s.slots - len(s.runs)
	var full []string
	for u, n := range s.ofUser {
		if n >= s.userSlots {
			full = append(full, u)
		}
	}
	running := len(s.runs)
	s.mu.Unlock()
	if free <= 0 {
		return nil
	}
	var runnable []store.Runnable
	err := s.st.Read(ctx, func(r store.Reader) (err error) {
		// The conversations running are runnable still, so as many more are asked for.
		runnable, err = r.Runnable(free+running, full)
		return err
	})
	if err != nil {
		return fmt.Errorf("schedule: the runnable conversations: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rn := range runnable {
		_, busy := s.runs[rn.Thread]
		if busy || len(s.runs) >= s.slots || s.ofUser[rn.User] >= s.userSlots {
			continue
		}
		s.start(ctx, rn)
	}
	return nil
}

var errLimit = errors.New("the turn reached its time limit")

func (s *Scheduler) start(ctx context.Context, rn store.Runnable) {
	wctx, cancel := context.WithTimeoutCause(ctx, s.limit, errLimit)
	w := &worker{user: rn.User, cancel: cancel}
	s.runs[rn.Thread] = w
	s.ofUser[rn.User]++
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		err := s.runner.Run(wctx, rn.Thread)
		timedOut := context.Cause(wctx) == errLimit
		cancel()
		s.mu.Lock()
		stopped := w.stopped
		s.mu.Unlock()
		// The turn ends before the slot is let go, or the conversation would be found runnable again first.
		if err != nil && ctx.Err() == nil && !stopped {
			reason := err.Error()
			if timedOut {
				reason = fmt.Sprintf("%v of %s", errLimit, s.limit)
			}
			if err := s.st.Update(ctx, func(tx store.Tx) error { return run.End(tx, rn.Thread, conversation.Failed, reason) }); err != nil {
				s.fail(fmt.Errorf("schedule: end the failed turn of thread %s: %w", rn.Thread, err))
			}
		}
		s.mu.Lock()
		delete(s.runs, rn.Thread)
		if s.ofUser[rn.User]--; s.ofUser[rn.User] == 0 {
			delete(s.ofUser, rn.User)
		}
		s.mu.Unlock()
		s.signal()
	}()
}

func (s *Scheduler) fail(err error) {
	select {
	case s.failed <- err:
	default:
	}
}

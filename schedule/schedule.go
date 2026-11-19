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
	"github.com/0xfakeSpike/ollupi/tool"
)

type Work struct {
	Noun  string
	Queue func(r store.Reader, limit int, full []string) ([]store.Runnable, error)
	Run   func(ctx context.Context, id string) error
	Fail  func(tx store.Tx, id, reason string) error
	Heard func(e store.Event) (wake bool, stop func(store.Runnable) bool)
}

func Threads(runThread func(ctx context.Context, thread string) error) Work {
	return Work{
		Noun:  "turn",
		Queue: store.Reader.Runnable,
		Run:   runThread,
		Fail:  failTurn,
		Heard: threadHeard,
	}
}

func failTurn(tx store.Tx, thread, reason string) error {
	return run.End(tx, thread, conversation.Failed, reason)
}

func threadHeard(e store.Event) (bool, func(store.Runnable) bool) {
	if e.Change == nil {
		return false, nil
	}
	runnable := e.Change.Header != nil && e.Change.Header.Runnable
	ended := slices.ContainsFunc(e.Change.Entries, endsTurn)
	if !ended && !e.Change.Deleted {
		return runnable, nil
	}
	return runnable, func(rn store.Runnable) bool { return rn.ID == e.Thread }
}

func endsTurn(entry conversation.Entry) bool {
	_, ok := entry.Body.(conversation.TurnEnded)
	return ok
}

func Tasks(runTask func(ctx context.Context, task string) error) Work {
	return Work{
		Noun:  "task",
		Queue: store.Reader.Tasks,
		Run:   runTask,
		Fail:  failTask,
		Heard: taskHeard,
	}
}

func failTask(tx store.Tx, task, reason string) error {
	return run.ReportTask(tx, task, tool.Result{Error: reason})
}

func taskHeard(e store.Event) (bool, func(store.Runnable) bool) {
	if e.Change == nil {
		return false, nil
	}
	launched := slices.ContainsFunc(e.Change.Entries, launchesTask)
	var reported []string
	for _, entry := range e.Change.Entries {
		if r, ok := entry.Body.(conversation.TaskReported); ok {
			reported = append(reported, r.Task)
		}
	}
	deleted := e.Change.Deleted
	if len(reported) == 0 && !deleted {
		return launched, nil
	}
	return launched, func(rn store.Runnable) bool {
		gone := deleted && rn.Thread == e.Thread
		return gone || slices.Contains(reported, rn.ID)
	}
}

func launchesTask(entry conversation.Entry) bool {
	_, ok := entry.Body.(conversation.TaskLaunched)
	return ok
}

type Scheduler struct {
	st        store.Store
	work      Work
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
	job     store.Runnable
	cancel  context.CancelFunc
	stopped bool
}

func New(st store.Store, work Work, slots, userSlots int, limit time.Duration) *Scheduler {
	return &Scheduler{st: st, work: work, slots: slots, userSlots: userSlots, limit: limit,
		wake: make(chan struct{}, 1), failed: make(chan error, 1), runs: map[string]*worker{}, ofUser: map[string]int{}}
}

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
	wake, stop := s.work.Heard(e)
	if wake {
		s.signal()
	}
	if stop == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.runs {
		if stop(w.job) {
			w.stopped = true
			w.cancel()
		}
	}
}

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
	var queued []store.Runnable
	err := s.st.Read(ctx, func(r store.Reader) (err error) {
		queued, err = s.work.Queue(r, free+running, full)
		return err
	})
	if err != nil {
		return fmt.Errorf("schedule: the queued %ss: %w", s.work.Noun, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range queued {
		_, busy := s.runs[job.ID]
		if busy || len(s.runs) >= s.slots || s.ofUser[job.User] >= s.userSlots {
			continue
		}
		s.start(ctx, job)
	}
	return nil
}

var errLimit = errors.New("the time limit")

func (s *Scheduler) start(ctx context.Context, job store.Runnable) {
	wctx, cancel := context.WithTimeoutCause(ctx, s.limit, errLimit)
	w := &worker{job: job, cancel: cancel}
	s.runs[job.ID] = w
	s.ofUser[job.User]++
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		err := s.work.Run(wctx, job.ID)
		timedOut := context.Cause(wctx) == errLimit
		cancel()
		s.mu.Lock()
		stopped := w.stopped
		s.mu.Unlock()
		if err != nil && ctx.Err() == nil && !stopped {
			reason := err.Error()
			if timedOut {
				reason = fmt.Sprintf("the %s reached its time limit of %s", s.work.Noun, s.limit)
			}
			if err := s.st.Update(ctx, func(tx store.Tx) error { return s.work.Fail(tx, job.ID, reason) }); err != nil {
				s.fail(fmt.Errorf("schedule: end the failed %s %s: %w", s.work.Noun, job.ID, err))
			}
		}
		s.mu.Lock()
		delete(s.runs, job.ID)
		if s.ofUser[job.User]--; s.ofUser[job.User] == 0 {
			delete(s.ofUser, job.User)
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

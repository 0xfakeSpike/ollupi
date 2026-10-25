package agent

import (
	"context"
	"errors"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"log"
	"sync"

	"github.com/0xfakeSpike/ollupi/repo"
)

var errStopped = errors.New("agent: the agent is shutting down")

type step struct {
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

type Agent struct {
	cfg      Config
	ctx      context.Context
	registry *tools.Registry
	skills   *skills.Catalog

	events       chan func()
	stepByThread map[string]*step
	done         sync.WaitGroup
}

func (a *Agent) start() {
	a.done.Add(1)
	go a.loop()
}

func (a *Agent) Wait() {
	a.done.Wait()
}

func (a *Agent) loop() {
	defer a.done.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case fn := <-a.events:
			fn()
		}
	}
}

func (a *Agent) post(fn func()) bool {
	select {
	case a.events <- fn:
		return true
	case <-a.ctx.Done():
		return false
	}
}

func (a *Agent) call(fn func() error) error {
	done := make(chan error, 1)
	if !a.post(func() { done <- fn() }) {
		return errStopped
	}
	select {
	case err := <-done:
		return err
	case <-a.ctx.Done():
		return errStopped
	}
}

func (a *Agent) enqueue(threadID, turnID string) {
	ctx, cancel := context.WithCancel(a.ctx)
	s := &step{turnID: turnID, ctx: ctx, cancel: cancel}
	a.stepByThread[threadID] = s
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		a.run(threadID, s)
	}()
}

func (a *Agent) run(threadID string, s *step) {
	if err := s.ctx.Err(); err != nil {
		a.post(func() { a.afterStep(threadID, s, 0, false, err) })
		return
	}
	view, err := a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read while turn %s is queued: %v", threadID, s.turnID, err)
		a.post(func() {
			a.release(threadID)
			a.failTurn(s.turnID, repo.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	turn, ok := findTurn(view, s.turnID)
	if !ok || turn.Status != repo.TurnInProgress {
		a.post(func() {
			a.release(threadID)
			a.reconcile(threadID)
		})
		return
	}
	final, usage, err := a.runStep(s.ctx, view, turn)
	if err := a.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		log.Printf("agent: thread %s: record usage of turn %s: %v", threadID, turn.ID, err)
	}
	seen := len(view.Items)
	a.post(func() { a.afterStep(threadID, s, seen, final, err) })
}

func (a *Agent) release(threadID string) {
	if s, exists := a.stepByThread[threadID]; exists {
		s.cancel()
		delete(a.stepByThread, threadID)
	}
}

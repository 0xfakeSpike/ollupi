package agent

import (
	"context"
	"errors"
	"log"

	"github.com/0xfakeSpike/ollupi/repo"
)

var errStopped = errors.New("agent: the agent is shutting down")

type slot struct {
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

type waiter struct {
	threadID string
	turnID   string
	subagent bool
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
	sl := &slot{turnID: turnID, ctx: ctx, cancel: cancel}
	a.slotByThread[threadID] = sl
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		a.run(threadID, sl)
	}()
}

func (a *Agent) run(threadID string, sl *slot) {
	if err := sl.ctx.Err(); err != nil {
		a.post(func() { a.afterStep(threadID, sl, 0, false, err) })
		return
	}
	view, err := a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read while turn %s is queued: %v", threadID, sl.turnID, err)
		a.post(func() {
			a.release(threadID)
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	turn, ok := findTurn(view, sl.turnID)
	if !ok || turn.Status != repo.TurnInProgress {
		a.post(func() {
			a.release(threadID)
			a.reconcile(threadID)
		})
		return
	}
	final, usage, err := a.runStep(sl.ctx, view, turn)
	if err := a.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		log.Printf("agent: thread %s: record usage of turn %s: %v", threadID, turn.ID, err)
	}
	seen := len(view.Items)
	a.post(func() { a.afterStep(threadID, sl, seen, final, err) })
}

func (a *Agent) release(threadID string) {
	if sl, exists := a.slotByThread[threadID]; exists {
		sl.cancel()
		delete(a.slotByThread, threadID)
		a.admit()
	}
}

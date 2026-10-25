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

const workers = 4

var errStopped = errors.New("agent: the agent is shutting down")

type phase int

const (
	queued phase = iota
	running
)

type slot struct {
	turnID string
	phase  phase
	ctx    context.Context
	cancel context.CancelFunc
}

type work struct {
	threadID string
	slot     *slot
}

type Agent struct {
	cfg      Config
	ctx      context.Context
	registry *tools.Registry
	skills   *skills.Catalog

	events chan func()
	work   chan work
	slots  map[string]*slot
	queue  []string
	done   sync.WaitGroup
}

func (a *Agent) start() {
	for range workers {
		a.done.Add(1)
		go a.worker()
	}
	a.done.Add(1)
	go a.loop()
}

func (a *Agent) Wait() {
	a.done.Wait()
}

func (a *Agent) loop() {
	defer a.done.Done()
	for {
		for len(a.queue) > 0 {
			if sl, ok := a.slots[a.queue[0]]; ok && sl.phase == queued {
				break
			}
			a.queue = a.queue[1:]
		}
		var out chan work
		var next work
		if len(a.queue) > 0 {
			out, next = a.work, work{threadID: a.queue[0], slot: a.slots[a.queue[0]]}
		}
		select {
		case <-a.ctx.Done():
			return
		case fn := <-a.events:
			fn()
		case out <- next:
			a.queue = a.queue[1:]
			next.slot.phase = running
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
	a.slots[threadID] = &slot{turnID: turnID, phase: queued, ctx: ctx, cancel: cancel}
	a.queue = append(a.queue, threadID)
}

func (a *Agent) worker() {
	defer a.done.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case w := <-a.work:
			a.run(w.threadID, w.slot)
		}
	}
}

func (a *Agent) run(threadID string, sl *slot) {
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
	outcome, usage, err := a.step(sl.ctx, view, turn)
	if err := a.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		log.Printf("agent: thread %s: record usage of turn %s: %v", threadID, turn.ID, err)
	}
	seen := len(view.Items)
	a.post(func() { a.afterStep(threadID, sl, seen, outcome, err) })
}

func (a *Agent) release(threadID string) {
	if sl, exists := a.slots[threadID]; exists {
		sl.cancel()
		delete(a.slots, threadID)
	}
}

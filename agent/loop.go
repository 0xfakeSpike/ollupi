package agent

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

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

type orchestrator struct {
	a   *Agent
	ctx context.Context

	events chan func()
	work   chan work
	slots  map[string]*slot
	queue  []string
	done   sync.WaitGroup
}

func newOrchestrator(a *Agent, ctx context.Context) *orchestrator {
	return &orchestrator{a: a, ctx: ctx, events: make(chan func()), work: make(chan work), slots: map[string]*slot{}}
}

func (s *orchestrator) start() {
	for range workers {
		s.done.Add(1)
		go s.worker()
	}
	s.done.Add(1)
	go s.loop()
}

func (s *orchestrator) wait() {
	s.done.Wait()
}

func (s *orchestrator) loop() {
	defer s.done.Done()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	s.sendDue("")
	for {
		for len(s.queue) > 0 {
			if sl, ok := s.slots[s.queue[0]]; ok && sl.phase == queued {
				break
			}
			s.queue = s.queue[1:]
		}
		var out chan work
		var next work
		if len(s.queue) > 0 {
			out, next = s.work, work{threadID: s.queue[0], slot: s.slots[s.queue[0]]}
		}
		select {
		case <-s.ctx.Done():
			return
		case fn := <-s.events:
			fn()
		case out <- next:
			s.queue = s.queue[1:]
			next.slot.phase = running
		case <-tick.C:
			s.sendDue("")
			s.reconcile()
		}
	}
}

func (s *orchestrator) post(fn func()) bool {
	select {
	case s.events <- fn:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *orchestrator) call(fn func() error) error {
	done := make(chan error, 1)
	if !s.post(func() { done <- fn() }) {
		return errStopped
	}
	select {
	case err := <-done:
		return err
	case <-s.ctx.Done():
		return errStopped
	}
}

func (s *orchestrator) enqueue(threadID, turnID string) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.slots[threadID] = &slot{turnID: turnID, phase: queued, ctx: ctx, cancel: cancel}
	s.queue = append(s.queue, threadID)
}

func (s *orchestrator) worker() {
	defer s.done.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case w := <-s.work:
			s.run(w.threadID, w.slot)
		}
	}
}

func (s *orchestrator) run(threadID string, sl *slot) {
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read while turn %s is queued: %v", threadID, sl.turnID, err)
		s.post(func() {
			s.release(threadID)
			s.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	turn, ok := findTurn(view, sl.turnID)
	if !ok || turn.Status != repo.TurnInProgress {
		s.post(func() {
			s.release(threadID)
			s.settle(threadID)
		})
		return
	}
	outcome, usage, err := s.a.step(sl.ctx, view, turn)
	if err := s.a.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		log.Printf("agent: thread %s: record usage of turn %s: %v", threadID, turn.ID, err)
	}
	seen := len(view.Items)
	s.post(func() { s.afterStep(threadID, sl, seen, outcome, err) })
}

func (s *orchestrator) release(threadID string) {
	if sl, exists := s.slots[threadID]; exists {
		sl.cancel()
		delete(s.slots, threadID)
	}
}

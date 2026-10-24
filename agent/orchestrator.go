package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
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

func (s *orchestrator) settle(threadID string) bool {
	if _, busy := s.slots[threadID]; busy || s.ctx.Err() != nil {
		return false
	}
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read to settle it: %v", threadID, err)
		return false
	}
	turn, active := view.ActiveTurn()
	if !active {
		s.sendDue(threadID)
		return false
	}
	unlaunched := slices.ContainsFunc(view.AgentCalls(), func(c repo.ItemRow) bool { return c.Await.ThreadID == "" })
	if s.settleAgentCalls(view) || unlaunched {
		s.launchSubagents(threadID)
		if view, err = s.a.ReadThread(threadID); err != nil {
			log.Printf("agent: thread %s cannot be read to settle it: %v", threadID, err)
			return false
		}
	}
	if _, waiting := view.PendingRequest(); waiting || len(view.AgentCalls()) > 0 {
		return false
	}
	s.enqueue(threadID, turn.ID)
	s.a.emitStatus(threadID)
	return true
}

func (s *orchestrator) reconcile() {
	active, err := s.a.DB.ActiveThreadIDs()
	if err != nil {
		log.Printf("agent: active threads: %v", err)
		return
	}
	for _, id := range active {
		s.settle(id)
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

func (s *orchestrator) failTurn(turnID, status, reason string) {
	turn, err := s.a.DB.Turn(turnID)
	if err != nil {
		log.Printf("agent: turn %s cannot be read to end it: %v", turnID, err)
		return
	}
	if turn.Status != repo.TurnInProgress {
		return
	}
	turn.Status, turn.Error = status, reason
	s.endTurn(turn)
}

func (s *orchestrator) afterStep(threadID string, sl *slot, seen int, outcome runtime.Outcome, stepErr error) {
	if s.slots[threadID] != sl {
		return
	}
	interrupted := sl.ctx.Err() != nil
	s.release(threadID)
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read after its step: %v", threadID, err)
		if interrupted {
			s.failTurn(sl.turnID, repo.TurnInterrupted, "interrupted by the user")
		} else {
			s.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage after its step")
		}
		return
	}
	turn, ok := view.ActiveTurn()
	if !ok || turn.ID != sl.turnID {
		s.settle(threadID)
		return
	}
	switch {
	case s.ctx.Err() != nil && (stepErr != nil || interrupted):
		log.Printf("agent: thread %s turn %s left in progress at shutdown: %v", threadID, turn.ID, stepErr)
	case interrupted:
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted by the user"
		s.endTurn(turn)
	case stepErr != nil:
		turn.Status, turn.Error = repo.TurnFailed, stepErr.Error()
		log.Printf("agent: thread %s turn %s failed: %v", threadID, turn.ID, stepErr)
		s.endTurn(turn)
	case outcome == runtime.Done && !view.InputAfter(turn.ID, seen):
		turn.Status = repo.TurnCompleted
		s.endTurn(turn)
	case !s.settle(threadID):
		if view, err := s.a.ReadThread(threadID); err == nil {
			s.a.emitRequests(view)
		}
		s.a.emitStatus(threadID)
	}
}

func (s *orchestrator) stop(threadID string, sl *slot) {
	if sl.phase == running {
		sl.cancel()
		return
	}
	s.queue = slices.DeleteFunc(s.queue, func(id string) bool { return id == threadID })
	s.release(threadID)
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read to stop it: %v", threadID, err)
		return
	}
	if turn, ok := view.ActiveTurn(); ok {
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted before it ran again"
		s.endTurn(turn)
	}
}

func (s *orchestrator) sendDue(threadID string) {
	if s.ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	due, err := s.a.DB.DueCronJobs(now)
	if err != nil {
		log.Printf("agent: scheduled tasks: %v", err)
		return
	}
	for _, job := range due {
		if threadID != "" && job.ThreadID != threadID {
			continue
		}
		view, err := s.a.ReadThread(job.ThreadID)
		if repo.IsNotFound(err) {
			continue
		}
		if err != nil {
			log.Printf("agent: scheduled task %s: read thread %s: %v", job.ID, job.ThreadID, err)
			continue
		}
		if _, active := view.ActiveTurn(); active {
			continue
		}
		if err := s.sendJob(view, job); err != nil {
			log.Printf("agent: scheduled task %s could not start in thread %s: %v", job.ID, job.ThreadID, err)
		}
		if err := s.a.cron.Sent(job, now); err != nil {
			log.Printf("agent: scheduled task %s: record its run: %v", job.ID, err)
		}
	}
}

func (s *orchestrator) sendJob(view repo.ThreadView, job repo.CronJobRow) error {
	text, err := checkMessage(job.Prompt)
	if err != nil {
		return err
	}
	model, err := s.a.conversationModel(view, "")
	if err != nil {
		return err
	}
	message := repo.NewUserMessage(uuid.NewString(), "", text, time.Now().UTC())
	message.CronJobID = job.ID
	_, err = s.beginTurn(view, message, model)
	return err
}

func (s *orchestrator) recover() error {
	active, err := s.a.DB.ActiveThreadIDs()
	if err != nil {
		return fmt.Errorf("agent: active threads: %w", err)
	}
	for _, id := range active {
		closed, err := s.a.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("agent: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			log.Printf("agent: thread %s: closed %d items cut off by the restart", id, len(closed))
		}
	}
	for _, id := range active {
		view, err := s.a.ReadThread(id)
		if err != nil {
			log.Printf("agent: thread %s cannot be read during recovery: %v", id, err)
			continue
		}
		if turn, running := view.ActiveTurn(); running {
			if _, err := s.a.modelOn(turn.Model.Connection); err != nil {
				turn.Status, turn.Error = repo.TurnFailed, err.Error()
				s.endTurn(turn)
				for _, call := range view.AgentCalls() {
					s.stopThread(call.Await.ThreadID)
				}
			}
		}
	}
	s.reconcile()
	return nil
}

func (s *orchestrator) settleAgentCalls(view repo.ThreadView) bool {
	settled := false
	for _, call := range view.AgentCalls() {
		it, ok := s.settledCall(call)
		if !ok {
			continue
		}
		stored, _, err := s.a.DB.RecordItem(view.Thread.ID, it)
		if err != nil {
			log.Printf("agent: thread %s: settle Agent call %s: %v", view.Thread.ID, call.ID, err)
			continue
		}
		turn, _ := view.ActiveTurn()
		s.a.emitItem(view.Thread, turn, stored, false)
		settled = true
	}
	return settled
}

func (s *orchestrator) settledCall(call repo.ItemRow) (repo.ItemRow, bool) {
	if call.Await.ThreadID == "" {
		return repo.ItemRow{}, false
	}
	if _, busy := s.slots[call.Await.ThreadID]; busy {
		return repo.ItemRow{}, false
	}
	child, err := s.a.ReadThread(call.Await.ThreadID)
	if repo.IsNotFound(err) {
		now := time.Now().UTC()
		call.Status, call.Error, call.CompletedAt = repo.ItemFailed, "the subagent's thread is missing", &now
		return call, true
	}
	if err != nil {
		return repo.ItemRow{}, false
	}
	if _, active := child.ActiveTurn(); active || len(child.Turns) == 0 {
		return repo.ItemRow{}, false
	}
	return report(call, child, child.Turns[len(child.Turns)-1]), true
}

func (a *Agent) StartTurn(threadID, text, connection string) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.run.call(func() (err error) {
		turn, err = a.run.startTurn(threadID, text, connection)
		return err
	})
	return turn, err
}

func (a *Agent) SteerTurn(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.run.call(func() (err error) {
		turn, err = a.run.steer(threadID, expectedTurnID, text)
		return err
	})
	return turn, err
}

func (a *Agent) InterruptTurn(threadID, expectedTurnID string) error {
	return a.run.call(func() error { return a.run.interrupt(threadID, expectedTurnID) })
}

package agent

import (
	"fmt"
	"log"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo"
)

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
		if turn, active = s.startQueued(view.Thread); !active {
			return false
		}
		if view, err = s.a.ReadThread(threadID); err != nil {
			log.Printf("agent: thread %s cannot be read to settle it: %v", threadID, err)
			return false
		}
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

func (s *orchestrator) startQueued(t repo.ThreadRow) (repo.TurnRow, bool) {
	turn, message, started, err := s.a.DB.StartQueuedTurn(t.ID)
	if err != nil {
		log.Printf("agent: thread %s: start its queued turn: %v", t.ID, err)
		return repo.TurnRow{}, false
	}
	if !started {
		return repo.TurnRow{}, false
	}
	s.a.emit(Event{Kind: EventTurnStarted, Thread: t, Turn: turn})
	s.a.emitItem(t, turn, message, true)
	return turn, true
}

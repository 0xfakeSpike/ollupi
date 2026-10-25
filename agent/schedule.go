package agent

import (
	"fmt"
	"log"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (s *orchestrator) schedule(threadID string) bool {
	if _, busy := s.slots[threadID]; busy || s.ctx.Err() != nil {
		return false
	}
	view, err := s.readThread(threadID)
	if err != nil {
		s.halt(threadID, err)
		return false
	}
	turn, active := view.ActiveTurn()
	if !active {
		return false
	}
	if _, waiting := view.PendingRequest(); waiting || len(view.AgentCalls()) > 0 {
		return false
	}
	s.enqueue(threadID, turn.ID)
	s.emitStatus(threadID)
	return true
}

func (s *orchestrator) promote(threadID string) {
	t, err := s.DB.ThreadRow(threadID)
	if err != nil {
		s.halt(threadID, err)
		return
	}
	turn, message, started, err := s.DB.StartQueuedTurn(threadID)
	if err != nil {
		s.halt(threadID, err)
		return
	}
	if started {
		s.emit(Event{Kind: EventTurnStarted, Thread: t, Turn: turn})
		s.emitItem(t, turn, message, true)
	}
}

func (s *orchestrator) launch(threadID string) {
	children, err := s.launchSubagents(threadID)
	for _, child := range children {
		s.schedule(child)
	}
	if err != nil {
		s.halt(threadID, err)
	}
}

func (s *orchestrator) halt(threadID string, cause error) {
	log.Printf("agent: thread %s cannot advance: %v", threadID, cause)
	turn, err := s.DB.ActiveTurn(threadID)
	if err != nil {
		s.fault(threadID, "", fmt.Errorf("the thread could not advance: %w", cause))
		return
	}
	turn.Status, turn.Error = repo.TurnFailed, "the agent could not advance this turn: "+cause.Error()
	s.endTurn(turn)
}

func (s *orchestrator) failTurn(turnID string, status repo.TurnStatus, reason string) {
	turn, err := s.DB.Turn(turnID)
	if err != nil {
		s.fault("", turnID, fmt.Errorf("the turn could not be read to end it (%s): %w", reason, err))
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
	view, err := s.readThread(threadID)
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
		s.promote(threadID)
		s.schedule(threadID)
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
	default:
		if outcome == runtime.Wait {
			s.launch(threadID)
		}
		if !s.schedule(threadID) {
			if view, err := s.readThread(threadID); err == nil {
				s.emitRequests(view)
			}
			s.emitStatus(threadID)
		}
	}
}

func (s *orchestrator) stop(threadID string, sl *slot) {
	if sl.phase == running {
		sl.cancel()
		return
	}
	s.queue = slices.DeleteFunc(s.queue, func(id string) bool { return id == threadID })
	s.release(threadID)
	view, err := s.readThread(threadID)
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
	active, err := s.DB.ActiveThreadIDs()
	if err != nil {
		return fmt.Errorf("agent: active threads: %w", err)
	}
	for _, id := range active {
		closed, err := s.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("agent: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			log.Printf("agent: thread %s: closed %d items cut off by the restart", id, len(closed))
		}
	}
	for _, id := range active {
		view, err := s.readThread(id)
		if err != nil {
			log.Printf("agent: thread %s cannot be read during recovery: %v", id, err)
			continue
		}
		if turn, running := view.ActiveTurn(); running {
			if _, err := s.modelOn(turn.Model.Connection); err != nil {
				turn.Status, turn.Error = repo.TurnFailed, err.Error()
				s.endTurn(turn)
				for _, call := range view.AgentCalls() {
					s.stopThread(call.Await.ThreadID)
				}
			}
		}
	}
	for _, id := range active {
		if view, err := s.readThread(id); err == nil {
			s.settleAgentCalls(view)
		}
		s.promote(id)
		s.launch(id)
		s.schedule(id)
	}
	return nil
}

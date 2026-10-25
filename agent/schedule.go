package agent

import (
	"fmt"
	"log"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) reconcile(threadID string) bool {
	if _, busy := a.slots[threadID]; busy || a.ctx.Err() != nil {
		return false
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		a.halt(threadID, err)
		return false
	}
	turn, active := st.ActiveTurn()
	if !active {
		if len(st.QueuedTurns()) == 0 {
			return false
		}
		var message repo.ItemRow
		if turn, message, err = a.cfg.DB.StartQueuedTurn(threadID); err != nil {
			a.halt(threadID, err)
			return false
		}
		a.emit(Event{Kind: EventTurnStarted, Thread: st.Thread, Turn: turn})
		a.emitItem(st.Thread, turn, message, true)
	}
	if _, err := a.modelOn(turn.Model.Connection); err != nil {
		turn.Status, turn.Error = repo.TurnFailed, err.Error()
		a.endTurn(turn)
		return false
	}
	waiting := len(st.PendingRequests()) > 0
	for _, call := range st.AgentCalls() {
		settled, err := a.settle(st.Thread, turn, call)
		if err != nil {
			a.halt(threadID, err)
			return false
		}
		waiting = waiting || !settled
	}
	if waiting {
		a.emitStatus(threadID)
		return false
	}
	a.enqueue(threadID, turn.ID)
	a.emitStatus(threadID)
	return true
}

func (a *Agent) halt(threadID string, cause error) {
	log.Printf("agent: thread %s cannot advance: %v", threadID, cause)
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if err != nil {
		a.fault(threadID, "", fmt.Errorf("the thread could not advance: %w", cause))
		return
	}
	turn.Status, turn.Error = repo.TurnFailed, "the agent could not advance this turn: "+cause.Error()
	a.endTurn(turn)
}

func (a *Agent) failTurn(turnID string, status repo.TurnStatus, reason string) {
	turn, err := a.cfg.DB.Turn(turnID)
	if err != nil {
		a.fault("", turnID, fmt.Errorf("the turn could not be read to end it (%s): %w", reason, err))
		return
	}
	if turn.Status != repo.TurnInProgress {
		return
	}
	turn.Status, turn.Error = status, reason
	a.endTurn(turn)
}

func (a *Agent) afterStep(threadID string, sl *slot, seen int, outcome runtime.Outcome, stepErr error) {
	if a.slots[threadID] != sl {
		return
	}
	interrupted := sl.ctx.Err() != nil
	a.release(threadID)
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read after its step: %v", threadID, err)
		if interrupted {
			a.failTurn(sl.turnID, repo.TurnInterrupted, "interrupted by the user")
		} else {
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage after its step")
		}
		return
	}
	turn, ok := st.ActiveTurn()
	if !ok || turn.ID != sl.turnID {
		a.reconcile(threadID)
		return
	}
	switch {
	case a.ctx.Err() != nil && (stepErr != nil || interrupted):
		log.Printf("agent: thread %s turn %s left in progress at shutdown: %v", threadID, turn.ID, stepErr)
		return
	case interrupted:
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted by the user"
	case stepErr != nil:
		turn.Status, turn.Error = repo.TurnFailed, stepErr.Error()
		log.Printf("agent: thread %s turn %s failed: %v", threadID, turn.ID, stepErr)
	case outcome == runtime.Done:
		more, err := a.cfg.DB.InputAfter(threadID, turn.ID, seen)
		if err != nil {
			a.halt(threadID, err)
			return
		}
		if !more {
			turn.Status = repo.TurnCompleted
		}
	}
	if turn.Status != repo.TurnInProgress {
		a.endTurn(turn)
		return
	}
	if !a.reconcile(threadID) {
		a.emitRequests(st)
	}
}

func (a *Agent) recover() error {
	active, err := a.cfg.DB.ActiveThreadIDs()
	if err != nil {
		return fmt.Errorf("agent: active threads: %w", err)
	}
	for _, id := range active {
		closed, err := a.cfg.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("agent: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			log.Printf("agent: thread %s: closed %d items cut off by the restart", id, len(closed))
		}
	}
	for _, id := range active {
		a.reconcile(id)
	}
	return nil
}

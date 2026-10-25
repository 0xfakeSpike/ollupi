package agent

import (
	"fmt"
	"log"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) schedule(threadID string) bool {
	if _, busy := a.slots[threadID]; busy || a.ctx.Err() != nil {
		return false
	}
	view, err := a.ReadThread(threadID)
	if err != nil {
		a.halt(threadID, err)
		return false
	}
	turn, active := view.ActiveTurn()
	if !active {
		return false
	}
	if _, waiting := view.PendingRequest(); waiting || len(view.AgentCalls()) > 0 {
		return false
	}
	a.enqueue(threadID, turn.ID)
	a.emitStatus(threadID)
	return true
}

func (a *Agent) promote(threadID string) {
	t, err := a.cfg.DB.ThreadRow(threadID)
	if err != nil {
		a.halt(threadID, err)
		return
	}
	turn, message, started, err := a.cfg.DB.StartQueuedTurn(threadID)
	if err != nil {
		a.halt(threadID, err)
		return
	}
	if started {
		a.emit(Event{Kind: EventTurnStarted, Thread: t, Turn: turn})
		a.emitItem(t, turn, message, true)
	}
}

func (a *Agent) launch(threadID string) {
	children, err := a.launchSubagents(threadID)
	for _, child := range children {
		a.schedule(child)
	}
	if err != nil {
		a.halt(threadID, err)
	}
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
	view, err := a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read after its step: %v", threadID, err)
		if interrupted {
			a.failTurn(sl.turnID, repo.TurnInterrupted, "interrupted by the user")
		} else {
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage after its step")
		}
		return
	}
	turn, ok := view.ActiveTurn()
	if !ok || turn.ID != sl.turnID {
		a.promote(threadID)
		a.schedule(threadID)
		return
	}
	switch {
	case a.ctx.Err() != nil && (stepErr != nil || interrupted):
		log.Printf("agent: thread %s turn %s left in progress at shutdown: %v", threadID, turn.ID, stepErr)
	case interrupted:
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted by the user"
		a.endTurn(turn)
	case stepErr != nil:
		turn.Status, turn.Error = repo.TurnFailed, stepErr.Error()
		log.Printf("agent: thread %s turn %s failed: %v", threadID, turn.ID, stepErr)
		a.endTurn(turn)
	case outcome == runtime.Done && !view.InputAfter(turn.ID, seen):
		turn.Status = repo.TurnCompleted
		a.endTurn(turn)
	default:
		if outcome == runtime.Wait {
			a.launch(threadID)
		}
		if !a.schedule(threadID) {
			if view, err := a.ReadThread(threadID); err == nil {
				a.emitRequests(view)
			}
			a.emitStatus(threadID)
		}
	}
}

func (a *Agent) stop(threadID string, sl *slot) {
	if sl.phase == running {
		sl.cancel()
		return
	}
	a.queue = slices.DeleteFunc(a.queue, func(id string) bool { return id == threadID })
	a.release(threadID)
	view, err := a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read to stop it: %v", threadID, err)
		return
	}
	if turn, ok := view.ActiveTurn(); ok {
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted before it ran again"
		a.endTurn(turn)
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
		view, err := a.ReadThread(id)
		if err != nil {
			log.Printf("agent: thread %s cannot be read during recovery: %v", id, err)
			continue
		}
		if turn, running := view.ActiveTurn(); running {
			if _, err := a.modelOn(turn.Model.Connection); err != nil {
				turn.Status, turn.Error = repo.TurnFailed, err.Error()
				a.endTurn(turn)
				for _, call := range view.AgentCalls() {
					a.stopThread(call.Await.ThreadID)
				}
			}
		}
	}
	for _, id := range active {
		if view, err := a.ReadThread(id); err == nil {
			a.settleAgentCalls(view)
		}
		a.promote(id)
		a.launch(id)
		a.schedule(id)
	}
	return nil
}

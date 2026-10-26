package agent

import (
	"fmt"
	"log"

	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) endTurn(turn repo.TurnRow) {
	st, err := a.cfg.DB.ThreadState(turn.ThreadID)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn could not be ended: %w", err))
		return
	}
	thread, ended, closed, err := a.cfg.DB.EndTurn(turn.ThreadID, turn)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the end of the turn could not be recorded: %w", err))
		return
	}
	for _, it := range closed {
		a.emitItem(thread, ended, it, false)
	}
	a.emit(Event{Kind: EventTurnCompleted, Thread: thread, Turn: ended})
	a.emitStatus(turn.ThreadID)
	for _, call := range st.AgentCalls() {
		a.stopThread(call.Await.ThreadID)
	}
	a.reconcile(turn.ThreadID)
	if st.Thread.ParentThreadID != "" {
		a.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (a *Agent) stopThread(threadID string) {
	if sl, ok := a.slotByThread[threadID]; ok {
		sl.cancel()
		return
	}
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if repo.IsNotFound(err) {
		return
	}
	if err != nil {
		a.fault(threadID, "", fmt.Errorf("the subagent could not be stopped: %w", err))
		return
	}
	turn.Status = repo.TurnInterrupted
	turn.Error = "stopped with the turn that launched it"
	a.endTurn(turn)
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

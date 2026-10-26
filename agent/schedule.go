package agent

import (
	"fmt"
	"log"
	"maps"
	"slices"

	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) reconcile(threadID string) bool {
	if _, busy := a.slotByThread[threadID]; busy || a.ctx.Err() != nil {
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
		if st.Thread, turn, message, err = a.cfg.DB.StartQueuedTurn(threadID); err != nil {
			a.halt(threadID, err)
			return false
		}
		a.emit(Event{Kind: EventTurnStarted, Thread: st.Thread, Turn: turn})
		a.emitItem(st.Thread, turn, message, true)
		a.emitStatus(threadID)
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
	if waiting || len(a.slotByThread) >= a.cfg.Slots {
		return false
	}
	a.enqueue(threadID, turn.ID)
	return true
}

func (a *Agent) admit() {
	if len(a.slotByThread) >= a.cfg.Slots || a.ctx.Err() != nil {
		return
	}
	ids, err := a.cfg.DB.OpenThreads(slices.Collect(maps.Keys(a.slotByThread)))
	if err != nil {
		a.fault("", "", fmt.Errorf("threads waiting for a slot could not be listed: %w", err))
		return
	}
	for _, id := range ids {
		if len(a.slotByThread) >= a.cfg.Slots {
			return
		}
		a.reconcile(id)
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
	a.admit()
	return nil
}

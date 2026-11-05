package agent

import (
	"fmt"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) reconcile(threadID string) {
	if _, busy := a.slotByThread[threadID]; busy || a.ctx.Err() != nil {
		return
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		a.halt(threadID, err)
		return
	}
	turn, active := st.ActiveTurn()
	if !active {
		if len(st.QueuedTurns()) == 0 {
			return
		}
		var message repo.ItemRow
		if st.Thread, turn, message, err = a.cfg.DB.StartQueuedTurn(threadID); err != nil {
			a.halt(threadID, err)
			return
		}
		a.startedTurn(st.Thread, turn)
		a.emitItem(st.Thread, turn, message, true)
		a.emitStatus(threadID)
	}
	if _, err := a.modelOn(turn.Model.Connection); err != nil {
		turn.Status, turn.Error = repo.TurnFailed, err.Error()
		a.endTurn(turn)
		return
	}
	reported := false
	for _, call := range st.AgentCalls() {
		settled, err := a.settle(st.Thread, turn, call)
		if err != nil {
			a.halt(threadID, err)
			return
		}
		reported = reported || settled
	}
	if reported {
		if turn, err = a.cfg.DB.Turn(turn.ID); err != nil {
			a.halt(threadID, err)
			return
		}
	}
	if turn.Status != repo.TurnReady || len(a.slotByThread) >= a.cfg.Slots || a.slotsOfUser[st.Thread.UserID] >= a.cfg.UserSlots {
		return
	}
	if err := a.cfg.DB.Launch(turn.ID); err != nil {
		a.halt(threadID, err)
		return
	}
	a.launch(st.Thread.UserID, threadID, turn.ID)
}

// admit fills the free slots with the threads whose turns are ready, reading no more of them than there are slots.
func (a *Agent) admit() {
	free := a.cfg.Slots - len(a.slotByThread)
	if free <= 0 || a.ctx.Err() != nil {
		return
	}
	var full []string
	for user, held := range a.slotsOfUser {
		if held >= a.cfg.UserSlots {
			full = append(full, user)
		}
	}
	ids, err := a.cfg.DB.ReadyThreads(free, full)
	if err != nil {
		a.fault("", "", fmt.Errorf("threads ready for a slot could not be listed: %w", err))
		return
	}
	for _, id := range ids {
		a.reconcile(id)
	}
}

// recover puts right what a stopped process left: the turns it was running leave their slots,
// and each thread it may have stranded is reconciled once.
func (a *Agent) recover() error {
	stranded, err := a.cfg.DB.StrandedThreads()
	if err != nil {
		return fmt.Errorf("agent: stranded threads: %w", err)
	}
	for _, id := range stranded {
		closed, err := a.cfg.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("agent: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			slog.Warn("items cut off by the restart closed", "thread", id, "items", len(closed))
		}
		if turn, err := a.cfg.DB.ActiveTurn(id); err == nil && turn.Status == repo.TurnInProgress {
			if _, err := a.cfg.DB.Park(turn.ID); err != nil {
				return fmt.Errorf("agent: recover thread %s: %w", id, err)
			}
		}
		a.reconcile(id)
	}
	a.admit()
	return nil
}

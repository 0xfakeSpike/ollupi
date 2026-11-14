package agent

import (
	"fmt"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo"
)

func (o *Orchestrator) reconcile(threadID string) {
	if _, busy := o.slotByThread[threadID]; busy || o.ctx.Err() != nil {
		return
	}
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		o.halt(threadID, err)
		return
	}
	turn, active := st.ActiveTurn()
	if !active {
		if len(st.QueuedTurns()) == 0 {
			return
		}
		var message repo.ItemRow
		if st.Thread, turn, message, err = o.cfg.DB.StartQueuedTurn(threadID); err != nil {
			o.halt(threadID, err)
			return
		}
		o.startedTurn(st.Thread, turn)
		o.emitItem(st.Thread, turn, message, true)
		o.emitStatus(threadID)
	}
	if _, err := o.modelOn(turn.Model.Connection); err != nil {
		turn.Status, turn.Error = repo.TurnFailed, err.Error()
		o.endTurn(turn)
		return
	}
	reported := false
	for _, call := range st.AgentCalls() {
		settled, err := o.settle(st.Thread, turn, call)
		if err != nil {
			o.halt(threadID, err)
			return
		}
		reported = reported || settled
	}
	if waits := st.BackgroundWaits(); len(waits) > 0 {
		o.deliverBackground(threadID)
		for _, call := range waits {
			fulfilled, err := o.fulfil(st.Thread, turn, call)
			if err != nil {
				o.halt(threadID, err)
				return
			}
			reported = reported || fulfilled
		}
	}
	if reported {
		if turn, err = o.cfg.DB.Turn(turn.ID); err != nil {
			o.halt(threadID, err)
			return
		}
	}
	if turn.Status != repo.TurnReady || len(o.slotByThread) >= o.cfg.Slots || o.slotsOfUser[st.Thread.UserID] >= o.cfg.UserSlots {
		return
	}
	if err := o.cfg.DB.Launch(turn.ID); err != nil {
		o.halt(threadID, err)
		return
	}
	o.launch(st.Thread.UserID, threadID, turn.ID)
}

// admit fills the free slots with the threads whose turns are ready, reading no more of them than there are slots.
func (o *Orchestrator) admit() {
	free := o.cfg.Slots - len(o.slotByThread)
	if free <= 0 || o.ctx.Err() != nil {
		return
	}
	var full []string
	for user, held := range o.slotsOfUser {
		if held >= o.cfg.UserSlots {
			full = append(full, user)
		}
	}
	ids, err := o.cfg.DB.ReadyThreads(free, full)
	if err != nil {
		o.fault("", "", fmt.Errorf("threads ready for a slot could not be listed: %w", err))
		return
	}
	for _, id := range ids {
		o.reconcile(id)
	}
}

// recover puts right what a stopped process left: the turns it was running leave their slots,
// and each thread it may have stranded is reconciled once.
func (o *Orchestrator) recover() error {
	stranded, err := o.cfg.DB.StrandedThreads()
	if err != nil {
		return fmt.Errorf("agent: stranded threads: %w", err)
	}
	for _, id := range stranded {
		closed, err := o.cfg.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("agent: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			slog.Warn("items cut off by the restart closed", "thread", id, "items", len(closed))
		}
		if turn, err := o.cfg.DB.ActiveTurn(id); err == nil && turn.Status == repo.TurnInProgress {
			if _, err := o.cfg.DB.Park(turn.ID); err != nil {
				return fmt.Errorf("agent: recover thread %s: %w", id, err)
			}
		}
		o.reconcile(id)
	}
	o.admit()
	return nil
}

package orchestrator

import (
	"errors"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) stopSubagent(threadID string) {
	if sl, ok := o.slotByThread[threadID]; ok {
		sl.cancel()
		return
	}
	turn, err := o.store.ActiveTurn(threadID)
	if errors.Is(err, db.ErrNotFound) {
		return
	}
	if err != nil {
		o.ev.Fault(threadID, "", "the subagent could not be stopped")
		return
	}
	turn.Status = db.TurnInterrupted
	turn.Error = "stopped with the turn that launched it"
	o.endTurn(turn)
}

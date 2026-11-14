package orchestrator

import (
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// settle reports to the parent the subagent that its call launched, once the subagent's turn ended. Until then it
// starts that turn, which the call queued, as a thread starts a queued turn: the orchestrator runs it when a slot is
// free. It reports whether it reported.
func (o *Orchestrator) settle(parent db.ThreadRow, turn db.TurnRow, call db.ItemRow) (bool, error) {
	report, progress, child, err := subagent.Report(o.cfg.DB, call)
	if err != nil {
		return false, err
	}
	switch progress {
	case subagent.Queued:
		o.emit(Event{Kind: EventThreadStarted, Thread: child})
		o.reconcile(child.ID)
		return false, nil
	case subagent.Running:
		return false, nil
	}
	stored, _, err := o.cfg.DB.RecordItem(parent.ID, report)
	if err != nil {
		return false, err
	}
	o.emitItem(parent, turn, stored, false)
	return true, nil
}

package orchestrator

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// announceSubagents tells of the subagents that the calls launched, as a conversation tells of a turn it starts. A turn
// runs only once the subagents it awaited reported, so the calls awaiting one after a run are the run's own: each is
// told of once. Their turns, written ready, take a slot as any other does.
func (o *Orchestrator) announceSubagents(calls []db.ItemRow) {
	for _, call := range calls {
		st, err := o.cfg.DB.ThreadState(call.Await.Handle)
		if err != nil {
			o.ev.Fault(call.Await.Handle, "", fmt.Errorf("the subagent could not be read: %w", err))
			continue
		}
		turn, active := st.ActiveTurn()
		if !active {
			continue
		}
		items, err := o.cfg.DB.TurnItems(turn.ID)
		if err != nil {
			o.ev.Fault(st.Thread.ID, turn.ID, fmt.Errorf("the subagent's task could not be read: %w", err))
			continue
		}
		o.ev.Emit(event.Event{Kind: event.ThreadStarted, Thread: st.Thread})
		o.ev.TurnStarted(st.Thread, turn)
		for _, it := range items {
			o.ev.Item(st.Thread, turn, it, true)
		}
		o.ev.Status(st.Thread.ID)
	}
}

// settle reports to the parent the subagent that its call launched, once the subagent's turn ended. It reports whether
// it reported.
func (o *Orchestrator) settle(parent db.ThreadRow, turn db.TurnRow, call db.ItemRow) (bool, error) {
	report, ended, err := subagent.Report(o.cfg.DB, call)
	if err != nil || !ended {
		return false, err
	}
	stored, _, err := o.cfg.DB.RecordItem(parent.ID, report)
	if err != nil {
		return false, err
	}
	o.ev.Item(parent, turn, stored, false)
	return true, nil
}

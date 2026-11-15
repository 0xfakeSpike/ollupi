package orchestrator

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
)

// SubagentStarted tells of a subagent that a call wrote, as a conversation tells of a turn it starts, and runs its
// turn when a slot is free. The call has returned already: the subagent reports to the turn that launched it as it
// ends (db.EndTurn).
func (o *Orchestrator) SubagentStarted(threadID string) {
	o.post(func() {
		st, err := o.cfg.DB.ThreadState(threadID)
		if err != nil {
			o.ev.Fault(threadID, "", fmt.Errorf("the subagent could not be read: %w", err))
			return
		}
		turn, active := st.ActiveTurn()
		if !active {
			return
		}
		items, err := o.cfg.DB.TurnItems(turn.ID)
		if err != nil {
			o.ev.Fault(threadID, turn.ID, fmt.Errorf("the subagent's task could not be read: %w", err))
			return
		}
		o.ev.Emit(event.Event{Kind: event.ThreadStarted, Thread: st.Thread})
		o.ev.TurnStarted(st.Thread, turn)
		for _, it := range items {
			o.ev.Item(st.Thread, turn, it, true)
		}
		o.ev.Status(threadID)
		o.reconcile(threadID)
	})
}

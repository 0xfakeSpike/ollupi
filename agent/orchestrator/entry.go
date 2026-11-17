package orchestrator

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) Wake(threadID string) { o.produce(wake{threadID}) }

// A turn written behind another is told of when the one ahead ends.
func (o *Orchestrator) Started(threadID string) { o.produce(started{threadID}) }

func (o *Orchestrator) Interrupt(threadID, expectedTurnID string) error {
	reply := make(chan error, 1)
	if !o.produce(interrupt{threadID: threadID, turnID: expectedTurnID, reply: reply}) {
		return errStopped
	}
	select {
	case err := <-reply:
		return err
	case <-o.ctx.Done():
		return errStopped
	}
}

func (o *Orchestrator) announce(threadID string) {
	st, err := o.store.ThreadState(threadID)
	if err != nil {
		o.ev.Fault(threadID, "", "the conversation could not be read")
		return
	}
	turn, active := st.ActiveTurn()
	if !active {
		return
	}
	items, err := o.store.TurnItems(turn.ID)
	if err != nil {
		o.ev.Fault(threadID, turn.ID, "the turn's message could not be read")
		return
	}
	o.ev.Emit(event.Event{Kind: event.TurnStarted, Thread: st.Thread, Turn: turn})
	for _, it := range items {
		o.ev.Item(st.Thread, turn, it, true)
	}
	o.ev.Status(threadID)
	o.reconcile(threadID)
}

func (o *Orchestrator) interrupt(threadID, expectedTurnID string) error {
	if sl, ok := o.slotByThread[threadID]; ok {
		if expectedTurnID != "" && sl.turnID != expectedTurnID {
			return fmt.Errorf("%w (expected %s, in progress %s)", db.ErrTurnMismatch, expectedTurnID, sl.turnID)
		}
		sl.cancel()
		return nil
	}
	st, err := o.store.ThreadState(threadID)
	if err != nil {
		return fmt.Errorf("orchestrator: thread %s: %w", threadID, err)
	}
	turn, active := st.ActiveTurn()
	if !active {
		return db.ErrNoActiveTurn
	}
	if expectedTurnID != "" && turn.ID != expectedTurnID {
		return fmt.Errorf("%w (expected %s, in progress %s)", db.ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	reason := map[db.TurnStatus]string{
		db.TurnReady:              "interrupted while waiting for a slot",
		db.TurnWaitingOnApproval:  "interrupted while waiting for the user",
		db.TurnWaitingOnUserInput: "interrupted while waiting for the user",
		db.TurnWaitingOnAgent:     "interrupted while waiting for subagents",
	}[turn.Status]
	turn.Status, turn.Error = db.TurnInterrupted, reason
	o.endTurn(turn)
	return nil
}

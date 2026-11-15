package orchestrator

import (
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

// What the service asks of the orchestrator: to look at a conversation again, whether it holds a slot, to interrupt
// its turn, and to stop or cancel background calls.

var (
	ErrNoActiveTurn = errors.New("the thread has no turn in progress")
	ErrTurnMismatch = errors.New("the expected turn is not the turn in progress")
)

// Wake looks at the conversation again: it starts a turn that became ready, takes the results that came back to it,
// and runs it when a slot is free.
func (o *Orchestrator) Wake(threadID string) {
	o.post(func() { o.reconcile(threadID) })
}

// Interrupt stops the conversation's turn in progress, which must be expectedTurnID when that is set: a run in its
// slot is stopped, and a turn that waits ends as interrupted.
func (o *Orchestrator) Interrupt(threadID, expectedTurnID string) error {
	return o.call(func() error { return o.interrupt(threadID, expectedTurnID) })
}

func (o *Orchestrator) interrupt(threadID, expectedTurnID string) error {
	if sl, ok := o.slotByThread[threadID]; ok {
		if expectedTurnID != "" && sl.turnID != expectedTurnID {
			return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, sl.turnID)
		}
		sl.cancel()
		return nil
	}
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		return fmt.Errorf("orchestrator: thread %s: %w", threadID, err)
	}
	turn, active := st.ActiveTurn()
	if !active {
		return ErrNoActiveTurn
	}
	if expectedTurnID != "" && turn.ID != expectedTurnID {
		return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	reason := map[db.TurnStatus]string{
		db.TurnReady:               "interrupted while waiting for a slot",
		db.TurnWaitingOnApproval:   "interrupted while waiting for the user",
		db.TurnWaitingOnUserInput:  "interrupted while waiting for the user",
		db.TurnWaitingOnAgent:      "interrupted while waiting for subagents",
		db.TurnWaitingOnBackground: "interrupted while waiting for background calls",
	}[turn.Status]
	turn.Status, turn.Error = db.TurnInterrupted, reason
	o.endTurn(turn)
	return nil
}

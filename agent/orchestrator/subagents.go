package orchestrator

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// settle reports to the parent the subagent that its call launched, once the subagent's turn ended. Until then it
// starts that turn, which the call queued, as a thread starts a queued turn: the orchestrator runs it when a slot is
// free. It reports whether it reported.
func (o *Orchestrator) settle(parent repo.ThreadRow, turn repo.TurnRow, call repo.ItemRow) (bool, error) {
	childID := call.Await.Handle
	child, err := o.cfg.DB.ThreadState(childID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: subagent thread %s: %w", childID, err)
	}
	if _, active := child.ActiveTurn(); active {
		return false, nil
	}
	if len(child.QueuedTurns()) > 0 {
		o.emit(Event{Kind: EventThreadStarted, Thread: child.Thread})
		o.reconcile(childID)
		return false, nil
	}
	if child.Last == nil {
		return false, fmt.Errorf("orchestrator: subagent thread %s has no turn", childID)
	}
	report, err := o.report(call, childID, *child.Last)
	if err != nil {
		return false, err
	}
	stored, _, err := o.cfg.DB.RecordItem(parent.ID, report)
	if err != nil {
		return false, err
	}
	o.emitItem(parent, turn, stored, false)
	return true, nil
}

func (o *Orchestrator) report(call repo.ItemRow, childID string, ended repo.TurnRow) (repo.ItemRow, error) {
	now := time.Now().UTC()
	call.CompletedAt = &now
	switch ended.Status {
	case repo.TurnCompleted:
		answer, err := o.cfg.DB.FinalAnswer(childID, ended.ID)
		if err != nil {
			return repo.ItemRow{}, fmt.Errorf("orchestrator: subagent %s completed turn %s without a final answer: %w", childID, ended.ID, err)
		}
		result, _, err := repo.ResultOf(tools.Result{Output: answer})
		if err != nil {
			return repo.ItemRow{}, err
		}
		call.Status, call.Result = repo.ItemCompleted, result
	case repo.TurnInterrupted:
		call.Status, call.Error = repo.ItemFailed, "the subagent was stopped: "+ended.Error
	default:
		call.Status, call.Error = repo.ItemFailed, "the subagent failed: "+ended.Error
	}
	return call, nil
}

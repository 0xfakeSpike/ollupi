package agent

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

func (a *Agent) settle(parent repo.ThreadRow, turn repo.TurnRow, call repo.ItemRow) (bool, error) {
	childID := call.Await.ThreadID
	if childID == "" {
		child, err := a.createThread(repo.ThreadRow{
			ParentThreadID: repo.Ref(parent.ID), ParentItemID: repo.Ref(call.ID),
			WorkspaceID: parent.WorkspaceID,
		})
		if err != nil {
			return false, err
		}
		a.emit(Event{Kind: EventThreadStarted, Thread: child})
		call.Await.ThreadID = child.ID
		stored, _, err := a.cfg.DB.RecordItem(parent.ID, call)
		if err != nil {
			return false, err
		}
		a.emitItem(parent, turn, stored, false)
		childID = child.ID
	}
	child, err := a.cfg.DB.ThreadState(childID)
	if err != nil {
		return false, fmt.Errorf("agent: subagent thread %s: %w", childID, err)
	}
	if len(child.Turns) == 0 {
		req, err := subagent.Decode(call.Await.Request)
		if err != nil {
			return false, err
		}
		if _, err := a.submit(child.Thread, req.Prompt, "", turn.Model); err != nil {
			return false, err
		}
		a.reconcile(childID)
		return false, nil
	}
	if _, active := child.ActiveTurn(); active {
		return false, nil
	}
	report, err := a.report(call, childID, child.Turns[len(child.Turns)-1])
	if err != nil {
		return false, err
	}
	stored, _, err := a.cfg.DB.RecordItem(parent.ID, report)
	if err != nil {
		return false, err
	}
	a.emitItem(parent, turn, stored, false)
	return true, nil
}

func (a *Agent) report(call repo.ItemRow, childID string, ended repo.TurnRow) (repo.ItemRow, error) {
	now := time.Now().UTC()
	call.CompletedAt = &now
	switch ended.Status {
	case repo.TurnCompleted:
		answer, err := a.cfg.DB.FinalAnswer(childID, ended.ID)
		if err != nil {
			return repo.ItemRow{}, fmt.Errorf("agent: subagent %s completed turn %s without a final answer: %w", childID, ended.ID, err)
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

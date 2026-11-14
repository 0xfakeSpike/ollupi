package agent

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func (o *Orchestrator) settle(parent repo.ThreadRow, turn repo.TurnRow, call repo.ItemRow) (bool, error) {
	req, err := role.Decode(call.Await.Request)
	if err != nil {
		return false, err
	}
	launched, ok := role.Launched(call.Tool)
	if !ok {
		return false, fmt.Errorf("agent: call %s of %q launches no subagent role", call.ID, call.Tool)
	}
	childID := call.Await.Handle
	if childID == "" {
		child, err := o.createThread(repo.ThreadRow{
			UserID:         parent.UserID,
			Title:          clipRunes(req.Description, maxTitleRunes),
			ParentThreadID: repo.Ref(parent.ID), ParentItemID: repo.Ref(call.ID),
			WorkspaceID:   parent.WorkspaceID,
			EnvironmentID: parent.EnvironmentID,
		})
		if err != nil {
			return false, err
		}
		call.Await.Handle = child.ID
		stored, _, err := o.cfg.DB.RecordItem(parent.ID, call)
		if err != nil {
			return false, err
		}
		o.emitItem(parent, turn, stored, false)
		childID = child.ID
	}
	child, err := o.cfg.DB.ThreadState(childID)
	if err != nil {
		return false, fmt.Errorf("agent: subagent thread %s: %w", childID, err)
	}
	if child.Last == nil {
		if _, err := o.submit(child.Thread, repo.TurnRow{Input: req.Prompt, Agent: string(launched.Name)}, turn.Model); err != nil {
			return false, err
		}
		o.reconcile(childID)
		return false, nil
	}
	if _, active := child.ActiveTurn(); active {
		return false, nil
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

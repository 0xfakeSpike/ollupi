package agent

import (
	"fmt"
	"log"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

type parentTurn struct {
	Thread repo.ThreadRow
	Turn   repo.TurnRow
}

func (a *Agent) launchSubagents(threadID string) ([]string, error) {
	view, err := a.ReadThread(threadID)
	if err != nil {
		return nil, err
	}
	turn, _ := view.ActiveTurn()
	var started []string
	for _, call := range view.AgentCalls() {
		child, err := a.launchSubagent(view.Thread, turn, &call)
		if err != nil {
			now := time.Now().UTC()
			call.Status, call.Error, call.CompletedAt = repo.ItemFailed, fmt.Sprintf("the subagent could not be launched: %v", err), &now
			stored, _, rerr := a.cfg.DB.RecordItem(threadID, call)
			if rerr != nil {
				return started, rerr
			}
			a.emitItem(view.Thread, turn, stored, false)
			continue
		}
		if child != "" {
			started = append(started, child)
		}
	}
	return started, nil
}

func (a *Agent) launchSubagent(parent repo.ThreadRow, parentTurn repo.TurnRow, call *repo.ItemRow) (string, error) {
	if parent.ParentThreadID != "" {
		return "", fmt.Errorf("a subagent cannot launch subagents; do the task yourself with your tools")
	}
	req, err := subagent.Decode(call.Await.Request)
	if err != nil {
		return "", err
	}
	childID := call.Await.ThreadID
	if childID == "" {
		child, err := a.createThread(repo.ThreadRow{
			ParentThreadID: repo.Ref(parent.ID), ParentItemID: repo.Ref(call.ID),
			WorkspaceID: parent.WorkspaceID,
		})
		if err != nil {
			return "", err
		}
		a.emit(Event{Kind: EventThreadStarted, Thread: child})
		call.Await.ThreadID = child.ID
		stored, _, err := a.cfg.DB.RecordItem(parent.ID, *call)
		if err != nil {
			return "", err
		}
		a.emitItem(parent, parentTurn, stored, false)
		childID = child.ID
	}
	view, err := a.ReadThread(childID)
	if err != nil {
		return "", err
	}
	if len(view.Turns) > 0 {
		return "", nil
	}
	if _, err := a.submit(view, req.Prompt, "", parentTurn.Model); err != nil {
		return "", err
	}
	return childID, nil
}

func (a *Agent) subagentReport(child repo.ThreadView, ended repo.TurnRow) (repo.ItemRow, parentTurn, bool, error) {
	if child.Thread.ParentThreadID == "" {
		return repo.ItemRow{}, parentTurn{}, false, nil
	}
	parent, err := a.ReadThread(string(child.Thread.ParentThreadID))
	if err != nil {
		return repo.ItemRow{}, parentTurn{}, false, err
	}
	turn, active := parent.ActiveTurn()
	if !active {
		return repo.ItemRow{}, parentTurn{}, false, nil
	}
	for _, call := range parent.AgentCalls() {
		if call.ID == string(child.Thread.ParentItemID) {
			return report(call, child, ended), parentTurn{Thread: parent.Thread, Turn: turn}, true, nil
		}
	}
	return repo.ItemRow{}, parentTurn{}, false, nil
}

func report(call repo.ItemRow, child repo.ThreadView, ended repo.TurnRow) repo.ItemRow {
	now := time.Now().UTC()
	call.CompletedAt = &now
	switch ended.Status {
	case repo.TurnCompleted:
		if answer, ok := child.FinalAnswer(ended.ID); ok {
			result, _, err := repo.ResultOf(tools.Result{Output: answer})
			if err == nil {
				call.Status, call.Result = repo.ItemCompleted, result
				return call
			}
		}
		call.Status, call.Error = repo.ItemFailed, "the subagent finished without a report"
	case repo.TurnInterrupted:
		call.Status, call.Error = repo.ItemFailed, "the subagent was stopped: "+ended.Error
	default:
		call.Status, call.Error = repo.ItemFailed, "the subagent failed: "+ended.Error
	}
	return call
}

func (a *Agent) settleAgentCalls(view repo.ThreadView) bool {
	settled := false
	for _, call := range view.AgentCalls() {
		it, ok := a.settledCall(call)
		if !ok {
			continue
		}
		stored, _, err := a.cfg.DB.RecordItem(view.Thread.ID, it)
		if err != nil {
			log.Printf("agent: thread %s: settle Agent call %s: %v", view.Thread.ID, call.ID, err)
			continue
		}
		turn, _ := view.ActiveTurn()
		a.emitItem(view.Thread, turn, stored, false)
		settled = true
	}
	return settled
}

func (a *Agent) settledCall(call repo.ItemRow) (repo.ItemRow, bool) {
	if call.Await.ThreadID == "" {
		return repo.ItemRow{}, false
	}
	if _, busy := a.slots[call.Await.ThreadID]; busy {
		return repo.ItemRow{}, false
	}
	child, err := a.ReadThread(call.Await.ThreadID)
	if repo.IsNotFound(err) {
		now := time.Now().UTC()
		call.Status, call.Error, call.CompletedAt = repo.ItemFailed, "the subagent's thread is missing", &now
		return call, true
	}
	if err != nil {
		return repo.ItemRow{}, false
	}
	if _, active := child.ActiveTurn(); active || len(child.Turns) == 0 {
		return repo.ItemRow{}, false
	}
	return report(call, child, child.Turns[len(child.Turns)-1]), true
}

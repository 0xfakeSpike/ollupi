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

func (s *orchestrator) launchSubagents(threadID string) {
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read to launch subagents: %v", threadID, err)
		return
	}
	turn, _ := view.ActiveTurn()
	for _, call := range view.AgentCalls() {
		if call.Await.ThreadID != "" {
			continue
		}
		if child, err := s.launchSubagent(view.Thread, turn, call); err != nil {
			now := time.Now().UTC()
			call.Status, call.Error, call.CompletedAt = repo.ItemFailed, fmt.Sprintf("the subagent could not be launched: %v", err), &now
		} else {
			call.Await.ThreadID = child.ID
		}
		stored, _, err := s.a.DB.RecordItem(threadID, call)
		if err != nil {
			log.Printf("agent: thread %s: record Agent call %s: %v", threadID, call.ID, err)
			continue
		}
		s.a.emitItem(view.Thread, turn, stored, false)
	}
}

func (s *orchestrator) launchSubagent(parent repo.ThreadRow, parentTurn repo.TurnRow, call repo.ItemRow) (repo.ThreadRow, error) {
	if parent.ParentThreadID != "" {
		return repo.ThreadRow{}, fmt.Errorf("a subagent cannot launch subagents; do the task yourself with your tools")
	}
	req, err := subagent.Decode(call.Await.Request)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	child, err := s.a.createThread(repo.ThreadRow{
		ParentThreadID: parent.ID, ParentItemID: call.ID,
		ApprovalPolicy: parent.ApprovalPolicy, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return repo.ThreadRow{}, err
	}
	s.a.emit(Event{Kind: EventThreadStarted, Thread: child})
	view, err := s.a.ReadThread(child.ID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if _, err := s.beginTurn(view, req.Prompt, "", parentTurn.Model); err != nil {
		return repo.ThreadRow{}, err
	}
	return child, nil
}

func (s *orchestrator) subagentReport(child repo.ThreadView, ended repo.TurnRow) (repo.ItemRow, parentTurn, bool) {
	if child.Thread.ParentThreadID == "" {
		return repo.ItemRow{}, parentTurn{}, false
	}
	parent, err := s.a.ReadThread(child.Thread.ParentThreadID)
	if err != nil {
		return repo.ItemRow{}, parentTurn{}, false
	}
	turn, active := parent.ActiveTurn()
	if !active {
		return repo.ItemRow{}, parentTurn{}, false
	}
	for _, call := range parent.AgentCalls() {
		if call.ID == child.Thread.ParentItemID {
			return report(call, child, ended), parentTurn{Thread: parent.Thread, Turn: turn}, true
		}
	}
	return repo.ItemRow{}, parentTurn{}, false
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

func (s *orchestrator) settleAgentCalls(view repo.ThreadView) bool {
	settled := false
	for _, call := range view.AgentCalls() {
		it, ok := s.settledCall(call)
		if !ok {
			continue
		}
		stored, _, err := s.a.DB.RecordItem(view.Thread.ID, it)
		if err != nil {
			log.Printf("agent: thread %s: settle Agent call %s: %v", view.Thread.ID, call.ID, err)
			continue
		}
		turn, _ := view.ActiveTurn()
		s.a.emitItem(view.Thread, turn, stored, false)
		settled = true
	}
	return settled
}

func (s *orchestrator) settledCall(call repo.ItemRow) (repo.ItemRow, bool) {
	if call.Await.ThreadID == "" {
		return repo.ItemRow{}, false
	}
	if _, busy := s.slots[call.Await.ThreadID]; busy {
		return repo.ItemRow{}, false
	}
	child, err := s.a.ReadThread(call.Await.ThreadID)
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

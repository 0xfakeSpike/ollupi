package agent

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/google/uuid"
)

type Input struct {
	Text       string
	Connection string
	CronJobID  string
}

func (a *Agent) StartTurn(threadID string, in Input) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.run.call(func() (err error) {
		turn, err = a.run.startTurn(threadID, in)
		return err
	})
	return turn, err
}

func (a *Agent) SteerTurn(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.run.call(func() (err error) {
		turn, err = a.run.steer(threadID, expectedTurnID, text)
		return err
	})
	return turn, err
}

func (a *Agent) InterruptTurn(threadID, expectedTurnID string) error {
	return a.run.call(func() error { return a.run.interrupt(threadID, expectedTurnID) })
}

func (a *Agent) ResolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.run.call(func() error { return a.run.resolveApproval(threadID, approvalID, decision, decidedBy) })
}

func (a *Agent) AnswerUserInput(threadID, itemID string, answers map[string]userinput.Answer) error {
	return a.run.call(func() error { return a.run.answerUserInput(threadID, itemID, answers) })
}

func (s *orchestrator) startTurn(threadID string, in Input) (repo.TurnRow, error) {
	text, err := checkMessage(in.Text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if view.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, errSubagentThread
	}
	model, err := s.a.conversationModel(view, in.Connection)
	if err != nil {
		return repo.TurnRow{}, err
	}
	return s.beginTurn(view, text, in.CronJobID, model)
}

func (s *orchestrator) beginTurn(view repo.ThreadView, text, cronJobID string, model llm.Model) (repo.TurnRow, error) {
	threadID := view.Thread.ID
	env := s.a.environmentOf(view.Thread)
	if view.Thread.ParentThreadID == "" && view.Thread.WorkspaceID != "" {
		if err := usable(view.Thread.WorkspaceID, env.workspace); err != nil {
			return repo.TurnRow{}, err
		}
	}
	turn := repo.TurnRow{
		ID: uuid.NewString(), Model: model.Clone(), Settings: env.settings,
		Input: text, CronJobID: cronJobID, StartedAt: time.Now().UTC(),
	}
	turn, message, err := s.a.DB.SubmitTurn(threadID, turn)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if message != nil {
		s.a.emit(Event{Kind: EventTurnStarted, Thread: view.Thread, Turn: turn})
		s.a.emitItem(view.Thread, turn, *message, true)
	} else {
		s.a.emit(Event{Kind: EventTurnQueued, Thread: view.Thread, Turn: turn})
	}
	if !s.settle(threadID) {
		s.a.emitStatus(threadID)
	}
	return turn, nil
}

func (s *orchestrator) steer(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	text, err := checkMessage(text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if view.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, errSubagentThread
	}
	turn, active := view.ActiveTurn()
	if !active {
		return repo.TurnRow{}, ErrNoActiveTurn
	}
	if turn.ID != expectedTurnID {
		return repo.TurnRow{}, fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	stored, isNew, err := s.a.DB.RecordItem(threadID, repo.NewUserMessage(uuid.NewString(), turn.ID, text, time.Now().UTC()))
	if err != nil {
		return repo.TurnRow{}, err
	}
	s.a.emitItem(view.Thread, turn, stored, isNew)
	return turn, nil
}

func (s *orchestrator) interrupt(threadID, expectedTurnID string) error {
	if sl, ok := s.slots[threadID]; ok {
		if expectedTurnID != "" && sl.turnID != expectedTurnID {
			return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, sl.turnID)
		}
		s.stop(threadID, sl)
		return nil
	}
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		return err
	}
	turn, active := view.ActiveTurn()
	if !active {
		return ErrNoActiveTurn
	}
	if expectedTurnID != "" && turn.ID != expectedTurnID {
		return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	calls := view.AgentCalls()
	turn.Status = repo.TurnInterrupted
	turn.Error = "interrupted while waiting for the user"
	if len(calls) > 0 {
		turn.Error = "interrupted while waiting for subagents"
	}
	s.endTurn(turn)
	for _, call := range calls {
		s.stopThread(call.Await.ThreadID)
	}
	return nil
}

func (s *orchestrator) stopThread(threadID string) {
	if sl, ok := s.slots[threadID]; ok {
		s.stop(threadID, sl)
		return
	}
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		return
	}
	if turn, active := view.ActiveTurn(); active {
		turn.Status = repo.TurnInterrupted
		turn.Error = "stopped with the turn that launched it"
		s.endTurn(turn)
	}
}

func (s *orchestrator) endTurn(turn repo.TurnRow) {
	view, err := s.a.ReadThread(turn.ThreadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read to end turn %s: %v", turn.ThreadID, turn.ID, err)
		return
	}
	var closed []repo.ItemRow
	report, parent, reports := s.subagentReport(view, turn)
	if reports {
		var stored repo.ItemRow
		closed, stored, err = s.a.DB.EndSubagentTurn(turn.ThreadID, turn, report)
		if err == nil {
			s.a.emitItem(parent.Thread, parent.Turn, stored, false)
		}
	} else {
		closed, err = s.a.DB.EndTurn(turn.ThreadID, turn)
	}
	if err != nil {
		log.Printf("agent: thread %s: record end of turn %s: %v", turn.ThreadID, turn.ID, err)
	}
	view, err = s.a.ReadThread(turn.ThreadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read after turn %s ended: %v", turn.ThreadID, turn.ID, err)
		return
	}
	for _, it := range closed {
		s.a.emitItem(view.Thread, turn, it, false)
	}
	ended, _ := findTurn(view, turn.ID)
	s.a.emit(Event{Kind: EventTurnCompleted, Thread: view.Thread, Turn: ended})
	s.a.emitStatus(turn.ThreadID)
	if reports && err == nil {
		s.settle(parent.Thread.ID)
	}
	s.settle(turn.ThreadID)
}

func (s *orchestrator) resolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return s.respond(threadID, func(it repo.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it repo.ItemRow) (repo.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return repo.ItemRow{}, fmt.Errorf("agent: decision %q is not one of %q", decision, available)
		}
		now := time.Now().UTC()
		it.Approval.Decision, it.Approval.DecidedBy, it.Approval.DecidedAt = decision, decidedBy, &now
		return it, nil
	})
}

func (s *orchestrator) answerUserInput(threadID, itemID string, answers map[string]userinput.Answer) error {
	return s.respond(threadID, func(it repo.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it repo.ItemRow) (repo.ItemRow, error) {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			return repo.ItemRow{}, err
		}
		if err := req.Check(answers, maxInputRunes); err != nil {
			return repo.ItemRow{}, fmt.Errorf("agent: %w", err)
		}
		result, _, err := repo.ResultOf(tools.Result{Output: answers})
		if err != nil {
			return repo.ItemRow{}, err
		}
		now := time.Now().UTC()
		it.Result, it.Status, it.CompletedAt = result, repo.ItemCompleted, &now
		return it, nil
	})
}

func (s *orchestrator) respond(threadID string, match func(repo.ItemRow) bool, apply func(repo.ItemRow) (repo.ItemRow, error)) error {
	view, err := s.a.ReadThread(threadID)
	if err != nil {
		return err
	}
	if _, scheduled := s.slots[threadID]; scheduled {
		return errNoSuchRequest
	}
	i := slices.IndexFunc(view.PendingRequests(), match)
	if i < 0 {
		return errNoSuchRequest
	}
	pending := view.PendingRequests()[i]
	activeTurn, _ := view.ActiveTurn()
	answered, err := apply(pending)
	if err != nil {
		return err
	}
	stored, _, err := s.a.DB.RecordItem(threadID, answered)
	if err != nil {
		return err
	}
	s.a.emitItem(view.Thread, activeTurn, stored, false)
	if !s.settle(threadID) {
		s.a.emitStatus(threadID)
	}
	return nil
}

func checkMessage(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("agent: empty message")
	}
	if n := len([]rune(text)); n > maxInputRunes {
		return "", fmt.Errorf("agent: message has %d characters; at most %d are accepted", n, maxInputRunes)
	}
	return text, nil
}

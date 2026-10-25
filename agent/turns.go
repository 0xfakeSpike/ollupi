package agent

import (
	"fmt"
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

func (s *orchestrator) startTurn(threadID string, in Input) (repo.TurnRow, error) {
	text, err := checkMessage(in.Text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	view, err := s.readThread(threadID)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if view.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, ErrSubagentThread
	}
	model, err := s.conversationModel(view, in.Connection)
	if err != nil {
		return repo.TurnRow{}, err
	}
	return s.beginTurn(view, text, in.CronJobID, model)
}

func (s *orchestrator) beginTurn(view repo.ThreadView, text, cronJobID string, model llm.Model) (repo.TurnRow, error) {
	turn, err := s.submit(view, text, cronJobID, model)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if !s.schedule(view.Thread.ID) {
		s.emitStatus(view.Thread.ID)
	}
	return turn, nil
}

func (s *orchestrator) submit(view repo.ThreadView, text, cronJobID string, model llm.Model) (repo.TurnRow, error) {
	threadID := view.Thread.ID
	env, err := s.environmentOf(view.Thread)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if view.Thread.ParentThreadID == "" && view.Thread.WorkspaceID != "" {
		if err := usable(string(view.Thread.WorkspaceID), env.workspace); err != nil {
			return repo.TurnRow{}, err
		}
	}
	turn := repo.TurnRow{
		ID: uuid.NewString(), Model: model.Clone(),
		Input: text, CronJobID: cronJobID, StartedAt: time.Now().UTC(),
	}
	turn, message, err := s.DB.SubmitTurn(threadID, turn)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if message != nil {
		s.emit(Event{Kind: EventTurnStarted, Thread: view.Thread, Turn: turn})
		s.emitItem(view.Thread, turn, *message, true)
	} else {
		s.emit(Event{Kind: EventTurnQueued, Thread: view.Thread, Turn: turn})
	}
	return turn, nil
}

func (s *orchestrator) steer(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	text, err := checkMessage(text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	view, err := s.readThread(threadID)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if view.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, ErrSubagentThread
	}
	turn, active := view.ActiveTurn()
	if !active {
		return repo.TurnRow{}, ErrNoActiveTurn
	}
	if turn.ID != expectedTurnID {
		return repo.TurnRow{}, fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	stored, isNew, err := s.DB.RecordItem(threadID, repo.NewUserMessage(uuid.NewString(), turn.ID, text, time.Now().UTC()))
	if err != nil {
		return repo.TurnRow{}, err
	}
	s.emitItem(view.Thread, turn, stored, isNew)
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
	view, err := s.readThread(threadID)
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
	view, err := s.readThread(threadID)
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
	view, err := s.readThread(turn.ThreadID)
	if err != nil {
		s.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn could not be ended: %w", err))
		return
	}
	report, parent, reports, err := s.subagentReport(view, turn)
	if err != nil {
		s.fault(turn.ThreadID, turn.ID, fmt.Errorf("the subagent's report could not be delivered: %w", err))
		return
	}
	var closed []repo.ItemRow
	if reports {
		var stored repo.ItemRow
		if closed, stored, err = s.DB.EndSubagentTurn(turn.ThreadID, turn, report); err == nil {
			s.emitItem(parent.Thread, parent.Turn, stored, false)
		}
	} else {
		closed, err = s.DB.EndTurn(turn.ThreadID, turn)
	}
	if err != nil {
		s.fault(turn.ThreadID, turn.ID, fmt.Errorf("the end of the turn could not be recorded: %w", err))
		return
	}
	if view, err = s.readThread(turn.ThreadID); err != nil {
		s.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn ended but could not be read back: %w", err))
	} else {
		for _, it := range closed {
			s.emitItem(view.Thread, turn, it, false)
		}
		ended, _ := findTurn(view, turn.ID)
		s.emit(Event{Kind: EventTurnCompleted, Thread: view.Thread, Turn: ended})
		s.emitStatus(turn.ThreadID)
	}
	if reports {
		s.schedule(parent.Thread.ID)
	}
	s.promote(turn.ThreadID)
	s.schedule(turn.ThreadID)
}

func (s *orchestrator) resolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return s.respond(threadID, func(it repo.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it repo.ItemRow) (repo.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return repo.ItemRow{}, fmt.Errorf("agent: %w: %q is not one of %q", ErrInvalidDecision, decision, available)
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
			return repo.ItemRow{}, fmt.Errorf("agent: %w: %v", ErrInvalidAnswer, err)
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
	view, err := s.readThread(threadID)
	if err != nil {
		return err
	}
	if _, scheduled := s.slots[threadID]; scheduled {
		return ErrNoSuchRequest
	}
	i := slices.IndexFunc(view.PendingRequests(), match)
	if i < 0 {
		return ErrNoSuchRequest
	}
	pending := view.PendingRequests()[i]
	activeTurn, _ := view.ActiveTurn()
	answered, err := apply(pending)
	if err != nil {
		return err
	}
	stored, _, err := s.DB.RecordItem(threadID, answered)
	if err != nil {
		return err
	}
	s.emitItem(view.Thread, activeTurn, stored, false)
	if !s.schedule(threadID) {
		s.emitStatus(threadID)
	}
	return nil
}

func checkMessage(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("agent: %w: empty message", ErrInvalidMessage)
	}
	if n := len([]rune(text)); n > maxInputRunes {
		return "", fmt.Errorf("agent: %w: %d characters; at most %d are accepted", ErrInvalidMessage, n, maxInputRunes)
	}
	return text, nil
}

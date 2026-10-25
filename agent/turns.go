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

func (a *Agent) StartTurn(threadID string, in Input) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.call(func() (err error) {
		turn, err = a.startTurn(threadID, in)
		return err
	})
	return turn, err
}

func (a *Agent) SteerTurn(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.call(func() (err error) {
		turn, err = a.steer(threadID, expectedTurnID, text)
		return err
	})
	return turn, err
}

func (a *Agent) InterruptTurn(threadID, expectedTurnID string) error {
	return a.call(func() error { return a.interrupt(threadID, expectedTurnID) })
}

func (a *Agent) ResolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.call(func() error { return a.resolveApproval(threadID, approvalID, decision, decidedBy) })
}

func (a *Agent) AnswerUserInput(threadID, itemID string, answers map[string]userinput.Answer) error {
	return a.call(func() error { return a.answerUserInput(threadID, itemID, answers) })
}

func (a *Agent) startTurn(threadID string, in Input) (repo.TurnRow, error) {
	text, err := checkMessage(in.Text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return repo.TurnRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if st.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, ErrSubagentThread
	}
	model, err := a.conversationModel(st.Turns, in.Connection)
	if err != nil {
		return repo.TurnRow{}, err
	}
	turn, err := a.submit(st.Thread, text, in.CronJobID, model)
	if err != nil {
		return repo.TurnRow{}, err
	}
	a.reconcile(threadID)
	return turn, nil
}

func (a *Agent) submit(t repo.ThreadRow, text, cronJobID string, model llm.Model) (repo.TurnRow, error) {
	if t.ParentThreadID == "" && t.WorkspaceID != "" {
		w, err := a.workspaceOf(t.WorkspaceID)
		if err != nil {
			return repo.TurnRow{}, err
		}
		if err := usable(string(t.WorkspaceID), w); err != nil {
			return repo.TurnRow{}, err
		}
	}
	turn := repo.TurnRow{
		ID: uuid.NewString(), Model: model.Clone(),
		Input: text, CronJobID: cronJobID, StartedAt: time.Now().UTC(),
	}
	turn, message, err := a.cfg.DB.SubmitTurn(t.ID, turn)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if message != nil {
		a.emit(Event{Kind: EventTurnStarted, Thread: t, Turn: turn})
		a.emitItem(t, turn, *message, true)
	} else {
		a.emit(Event{Kind: EventTurnQueued, Thread: t, Turn: turn})
	}
	return turn, nil
}

func (a *Agent) steer(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	text, err := checkMessage(text)
	if err != nil {
		return repo.TurnRow{}, err
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return repo.TurnRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if st.Thread.ParentThreadID != "" {
		return repo.TurnRow{}, ErrSubagentThread
	}
	turn, active := st.ActiveTurn()
	if !active {
		return repo.TurnRow{}, ErrNoActiveTurn
	}
	if turn.ID != expectedTurnID {
		return repo.TurnRow{}, fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	stored, isNew, err := a.cfg.DB.RecordItem(threadID, repo.NewUserMessage(uuid.NewString(), turn.ID, text, time.Now().UTC()))
	if err != nil {
		return repo.TurnRow{}, err
	}
	a.emitItem(st.Thread, turn, stored, isNew)
	return turn, nil
}

func (a *Agent) interrupt(threadID, expectedTurnID string) error {
	if sl, ok := a.slots[threadID]; ok {
		if expectedTurnID != "" && sl.turnID != expectedTurnID {
			return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, sl.turnID)
		}
		a.stop(threadID, sl)
		return nil
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	turn, active := st.ActiveTurn()
	if !active {
		return ErrNoActiveTurn
	}
	if expectedTurnID != "" && turn.ID != expectedTurnID {
		return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	turn.Status = repo.TurnInterrupted
	turn.Error = "interrupted while waiting for the user"
	if len(st.AgentCalls()) > 0 {
		turn.Error = "interrupted while waiting for subagents"
	}
	a.endTurn(turn)
	return nil
}

func (a *Agent) stopThread(threadID string) {
	if sl, ok := a.slots[threadID]; ok {
		a.stop(threadID, sl)
		return
	}
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if repo.IsNotFound(err) {
		return
	}
	if err != nil {
		a.fault(threadID, "", fmt.Errorf("the subagent could not be stopped: %w", err))
		return
	}
	turn.Status = repo.TurnInterrupted
	turn.Error = "stopped with the turn that launched it"
	a.endTurn(turn)
}

func (a *Agent) endTurn(turn repo.TurnRow) {
	st, err := a.cfg.DB.ThreadState(turn.ThreadID)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn could not be ended: %w", err))
		return
	}
	ended, closed, err := a.cfg.DB.EndTurn(turn.ThreadID, turn)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the end of the turn could not be recorded: %w", err))
		return
	}
	for _, it := range closed {
		a.emitItem(st.Thread, ended, it, false)
	}
	a.emit(Event{Kind: EventTurnCompleted, Thread: st.Thread, Turn: ended})
	a.emitStatus(turn.ThreadID)
	for _, call := range st.AgentCalls() {
		a.stopThread(call.Await.ThreadID)
	}
	a.reconcile(turn.ThreadID)
	if st.Thread.ParentThreadID != "" {
		a.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (a *Agent) resolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.respond(threadID, func(it repo.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it repo.ItemRow) (repo.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return repo.ItemRow{}, fmt.Errorf("agent: %w: %q is not one of %q", ErrInvalidDecision, decision, available)
		}
		now := time.Now().UTC()
		it.Approval.Decision, it.Approval.DecidedBy, it.Approval.DecidedAt = decision, decidedBy, &now
		return it, nil
	})
}

func (a *Agent) answerUserInput(threadID, itemID string, answers map[string]userinput.Answer) error {
	return a.respond(threadID, func(it repo.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it repo.ItemRow) (repo.ItemRow, error) {
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

func (a *Agent) respond(threadID string, match func(repo.ItemRow) bool, apply func(repo.ItemRow) (repo.ItemRow, error)) error {
	if _, scheduled := a.slots[threadID]; scheduled {
		return ErrNoSuchRequest
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	pending := st.PendingRequests()
	i := slices.IndexFunc(pending, match)
	if i < 0 {
		return ErrNoSuchRequest
	}
	activeTurn, _ := st.ActiveTurn()
	answered, err := apply(pending[i])
	if err != nil {
		return err
	}
	stored, _, err := a.cfg.DB.RecordItem(threadID, answered)
	if err != nil {
		return err
	}
	a.emitItem(st.Thread, activeTurn, stored, false)
	a.reconcile(threadID)
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

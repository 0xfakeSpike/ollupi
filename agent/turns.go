package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Input struct {
	Text        string
	Attachments []AttachmentInput
	Connection  string
	CronJobID   string
}

func (a *Agent) StartTurn(ctx context.Context, user, threadID string, in Input) (repo.TurnRow, error) {
	atts, err := a.attach(ctx, user, threadID, in.Attachments)
	if err != nil {
		return repo.TurnRow{}, err
	}
	var turn repo.TurnRow
	err = a.call(func() (err error) {
		turn, err = a.startTurn(threadID, in, atts)
		return err
	})
	a.dropUnsent(atts, err)
	if err == nil {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("ollupi.turn.id", turn.ID))
		span.AddLink(trace.Link{SpanContext: turnTrace(turn.ID)})
	}
	return turn, err
}

// StartScheduledTurn runs a scheduled task in its thread, whose owner set it; no caller asks for this one.
func (a *Agent) StartScheduledTurn(threadID, text, jobID string) error {
	return a.call(func() error {
		_, err := a.startTurn(threadID, Input{Text: text, CronJobID: jobID}, nil)
		return err
	})
}

func (a *Agent) SteerTurn(ctx context.Context, user, threadID, expectedTurnID string, in Input) (repo.TurnRow, error) {
	atts, err := a.attach(ctx, user, threadID, in.Attachments)
	if err != nil {
		return repo.TurnRow{}, err
	}
	var turn repo.TurnRow
	err = a.call(func() (err error) {
		turn, err = a.steer(threadID, expectedTurnID, in, atts)
		return err
	})
	a.dropUnsent(atts, err)
	if err == nil {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("ollupi.turn.id", turn.ID))
		span.AddLink(trace.Link{SpanContext: turnTrace(turn.ID)})
	}
	return turn, err
}

// dropUnsent removes the attachments of a message that was refused. When the agent stopped, the loop may have taken
// the message all the same, so they stay; deleting the thread removes them if not.
func (a *Agent) dropUnsent(atts []repo.Attachment, err error) {
	if err != nil && !errors.Is(err, errStopped) {
		a.removeAttachments(atts)
	}
}

func (a *Agent) InterruptTurn(user, threadID, expectedTurnID string) error {
	return a.call(func() error {
		if err := a.owned(user, threadID); err != nil {
			return err
		}
		return a.interrupt(threadID, expectedTurnID)
	})
}

func (a *Agent) ResolveApproval(user, threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.call(func() error {
		if err := a.owned(user, threadID); err != nil {
			return err
		}
		return a.resolveApproval(threadID, approvalID, decision, decidedBy)
	})
}

func (a *Agent) AnswerUserInput(user, threadID, itemID string, answers userinput.Answers) error {
	return a.call(func() error {
		if err := a.owned(user, threadID); err != nil {
			return err
		}
		return a.answerUserInput(threadID, itemID, answers)
	})
}

// SkipUserInput declines a question the agent asked: the call ends declined, telling the model the user chose not to
// answer, and the turn goes on.
func (a *Agent) SkipUserInput(user, threadID, itemID string) error {
	return a.call(func() error {
		if err := a.owned(user, threadID); err != nil {
			return err
		}
		return a.respond(threadID, func(it repo.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it repo.ItemRow) (repo.ItemRow, error) {
			now := time.Now().UTC()
			it.Status, it.Error, it.CompletedAt = repo.ItemDeclined, "the user skipped these questions; go on without their answers", &now
			return it, nil
		})
	})
}

func (a *Agent) startTurn(threadID string, in Input, atts []repo.Attachment) (repo.TurnRow, error) {
	text, err := checkMessage(in.Text, len(in.Attachments))
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
	if len(st.QueuedTurns()) >= maxQueuedTurns {
		return repo.TurnRow{}, ErrQueueFull
	}
	model, err := a.conversationModel(st.Last, in.Connection)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if err := a.checkLength(model, text); err != nil {
		return repo.TurnRow{}, err
	}
	if err := a.checkImages(threadID, model, atts); err != nil {
		return repo.TurnRow{}, err
	}
	window, err := a.windowFor(model)
	if err != nil {
		return repo.TurnRow{}, err
	}
	main := role.ForMain(window.InputTokens())
	return a.submit(st.Thread, repo.TurnRow{Input: text, Attachments: atts, CronJobID: in.CronJobID, Agent: string(main.Name)}, model)
}

func (a *Agent) submit(t repo.ThreadRow, turn repo.TurnRow, model llm.Model) (repo.TurnRow, error) {
	if t.ArchivedAt != nil {
		return repo.TurnRow{}, ErrThreadArchived
	}
	if t.ParentThreadID == "" && t.WorkspaceID != "" {
		w, err := a.workspaceOf(t.WorkspaceID)
		if err != nil {
			return repo.TurnRow{}, err
		}
		if err := usable(w); err != nil {
			return repo.TurnRow{}, err
		}
		a.moveMu.Lock()
		defer a.moveMu.Unlock()
		if a.moving[w.ID] {
			return repo.TurnRow{}, fmt.Errorf("agent: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
		}
	}
	now := time.Now().UTC()
	turn.ID, turn.Model, turn.CreatedAt, turn.StartedAt = uuid.NewString(), model.Clone(), now, now
	t, turn, message, err := a.cfg.DB.SubmitTurn(t.ID, turn)
	if err != nil {
		return repo.TurnRow{}, err
	}
	if message != nil {
		a.startedTurn(t, turn)
		a.emitItem(t, turn, *message, true)
		a.emitStatus(t.ID)
	} else {
		slog.Info("turn queued", "thread", t.ID, "turn", turn.ID)
		a.emit(Event{Kind: EventTurnQueued, Thread: t, Turn: turn})
	}
	return turn, nil
}

func (a *Agent) steer(threadID, expectedTurnID string, in Input, atts []repo.Attachment) (repo.TurnRow, error) {
	text, err := checkMessage(in.Text, len(in.Attachments))
	if err != nil {
		return repo.TurnRow{}, err
	}
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		return repo.TurnRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	turn, active := st.ActiveTurn()
	if !active {
		return repo.TurnRow{}, ErrNoActiveTurn
	}
	if turn.ID != expectedTurnID {
		return repo.TurnRow{}, fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	if err := a.checkLength(turn.Model, text); err != nil {
		return repo.TurnRow{}, err
	}
	if err := a.checkImages(threadID, turn.Model, atts); err != nil {
		return repo.TurnRow{}, err
	}
	stored, closed, err := a.cfg.DB.Steer(threadID, repo.NewUserMessage(uuid.NewString(), turn.ID, text, atts, time.Now().UTC()))
	if err != nil {
		return repo.TurnRow{}, err
	}
	a.emitItem(st.Thread, turn, stored, true)
	for _, it := range closed {
		a.emitItem(st.Thread, turn, it, false)
	}
	if len(closed) > 0 {
		slog.Info("turn steered out of its wait", "thread", threadID, "turn", turn.ID, "declined", len(closed))
		a.emitStatus(threadID)
	}
	return turn, nil
}

func (a *Agent) interrupt(threadID, expectedTurnID string) error {
	if sl, ok := a.slotByThread[threadID]; ok {
		if expectedTurnID != "" && sl.turnID != expectedTurnID {
			return fmt.Errorf("%w (expected %s, in progress %s)", ErrTurnMismatch, expectedTurnID, sl.turnID)
		}
		sl.cancel()
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
	reason := map[repo.TurnStatus]string{
		repo.TurnReady:              "interrupted while waiting for a slot",
		repo.TurnWaitingOnApproval:  "interrupted while waiting for the user",
		repo.TurnWaitingOnUserInput: "interrupted while waiting for the user",
		repo.TurnWaitingOnAgent:     "interrupted while waiting for subagents",
	}[turn.Status]
	turn.Status, turn.Error = repo.TurnInterrupted, reason
	a.endTurn(turn)
	return nil
}

func (a *Agent) resolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.respond(threadID, func(it repo.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it repo.ItemRow) (repo.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return repo.ItemRow{}, fmt.Errorf("agent: %w: %q is not one of %q", ErrInvalidDecision, decision, available)
		}
		if decision == repo.DecisionAcceptAlways {
			if err := a.allowAlways(threadID, it.Approval.Suggested); err != nil {
				return repo.ItemRow{}, err
			}
		}
		now := time.Now().UTC()
		it.Approval.Decision, it.Approval.DecidedBy, it.Approval.DecidedAt = decision, decidedBy, &now
		return it, nil
	})
}

func (a *Agent) allowAlways(threadID string, rules []string) error {
	t, err := a.cfg.DB.ThreadRow(threadID)
	if err != nil {
		return fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if parent := string(t.ParentThreadID); parent != "" {
		if t, err = a.cfg.DB.ThreadRow(parent); err != nil {
			return fmt.Errorf("agent: parent thread %s: %w", parent, err)
		}
	}
	allow := permission.Settings{Rules: permission.Rules{Allow: rules}}
	if t.WorkspaceID == "" {
		_, err := a.UpdateThreadSettings(t.UserID, t.ID, permission.Resolve(t.Settings, allow))
		return err
	}
	w, err := a.workspaceOf(t.WorkspaceID)
	if err != nil {
		return err
	}
	_, err = a.UpdateWorkspaceSettings(t.UserID, w.ID, permission.Resolve(w.Settings, allow))
	return err
}

func (a *Agent) answerUserInput(threadID, itemID string, answers userinput.Answers) error {
	return a.respond(threadID, func(it repo.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it repo.ItemRow) (repo.ItemRow, error) {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			return repo.ItemRow{}, err
		}
		if err := req.Check(answers, maxAnswerRunes); err != nil {
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
	if _, scheduled := a.slotByThread[threadID]; scheduled {
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
	a.emitStatus(threadID)
	return nil
}

func checkMessage(text string, attachments int) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" && attachments == 0 {
		return "", fmt.Errorf("agent: %w: empty message", ErrInvalidMessage)
	}
	return text, nil
}

// checkLength holds a message to the share of the model's input one message may take, so that it can always be sent.
func (a *Agent) checkLength(model llm.Model, text string) error {
	window, err := a.windowFor(model)
	if err != nil {
		return err
	}
	if tokens, budget := llm.EstimateTokens(text), window.MessageBudget(); tokens > budget {
		return fmt.Errorf("agent: %w: the message takes about %d tokens, and model %s takes at most %d in one message; send long text as a file attachment",
			ErrInvalidMessage, tokens, model.Name, budget)
	}
	return nil
}

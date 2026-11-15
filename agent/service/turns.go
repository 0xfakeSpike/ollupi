package service

import (
	"context"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
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

func (sv *Service) StartTurn(ctx context.Context, user, threadID string, in Input) (db.TurnRow, error) {
	atts, err := sv.attach(ctx, user, threadID, in.Attachments)
	if err != nil {
		return db.TurnRow{}, err
	}
	turn, err := sv.startTurn(threadID, in, atts)
	if err != nil {
		sv.removeAttachments(atts)
		return db.TurnRow{}, err
	}
	linkTurn(ctx, turn)
	return turn, nil
}

// StartScheduledTurn runs a scheduled task in its thread, whose owner set it; no caller asks for this one.
func (sv *Service) StartScheduledTurn(threadID, text, jobID string) error {
	_, err := sv.startTurn(threadID, Input{Text: text, CronJobID: jobID}, nil)
	return err
}

func (sv *Service) SteerTurn(ctx context.Context, user, threadID, expectedTurnID string, in Input) (db.TurnRow, error) {
	atts, err := sv.attach(ctx, user, threadID, in.Attachments)
	if err != nil {
		return db.TurnRow{}, err
	}
	turn, err := sv.steer(threadID, expectedTurnID, in, atts)
	if err != nil {
		sv.removeAttachments(atts)
		return db.TurnRow{}, err
	}
	linkTurn(ctx, turn)
	return turn, nil
}

// linkTurn names the turn on the request's span and links it to the turn's trace, where the turn's runs are.
func linkTurn(ctx context.Context, turn db.TurnRow) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("ollupi.turn.id", turn.ID))
	span.AddLink(trace.Link{SpanContext: orchestrator.TurnTrace(turn.ID)})
}

func (sv *Service) InterruptTurn(user, threadID, expectedTurnID string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.o.Interrupt(threadID, expectedTurnID)
}

func (sv *Service) ResolveApproval(user, threadID, approvalID string, decision db.Decision, decidedBy string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.resolveApproval(threadID, approvalID, decision, decidedBy)
}

func (sv *Service) AnswerUserInput(user, threadID, itemID string, answers userinput.Answers) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.answerUserInput(threadID, itemID, answers)
}

// SkipUserInput declines a question the agent asked: the call ends declined, telling the model the user chose not to
// answer, and the turn goes on.
func (sv *Service) SkipUserInput(user, threadID, itemID string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.answer(threadID, func(it db.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it db.ItemRow) (db.ItemRow, error) {
		now := time.Now().UTC()
		it.Status, it.Error, it.CompletedAt = db.ItemDeclined, "the user skipped these questions; go on without their answers", &now
		return it, nil
	})
}

func (sv *Service) startTurn(threadID string, in Input, atts []db.Attachment) (db.TurnRow, error) {
	text, err := checkMessage(in.Text, len(in.Attachments))
	if err != nil {
		return db.TurnRow{}, err
	}
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return db.TurnRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	if st.Thread.ParentThreadID != "" {
		return db.TurnRow{}, ErrSubagentThread
	}
	if len(st.QueuedTurns()) >= maxQueuedTurns {
		return db.TurnRow{}, ErrQueueFull
	}
	model, err := sv.conversationModel(st.Last, in.Connection)
	if err != nil {
		return db.TurnRow{}, err
	}
	if err := sv.checkLength(model, text); err != nil {
		return db.TurnRow{}, err
	}
	if err := sv.checkImages(threadID, model, atts); err != nil {
		return db.TurnRow{}, err
	}
	main, err := sv.mainRole(model)
	if err != nil {
		return db.TurnRow{}, err
	}
	return sv.submit(st.Thread, db.TurnRow{Input: text, Attachments: atts, CronJobID: in.CronJobID, Agent: string(main.Name)}, model)
}

func (sv *Service) submit(t db.ThreadRow, turn db.TurnRow, model llm.Model) (db.TurnRow, error) {
	if t.ParentThreadID == "" && t.WorkspaceID != "" {
		w, err := sv.workspaceOf(t.WorkspaceID)
		if err != nil {
			return db.TurnRow{}, err
		}
		if err := usable(w); err != nil {
			return db.TurnRow{}, err
		}
		sv.moveMu.Lock()
		defer sv.moveMu.Unlock()
		if sv.moving[w.ID] {
			return db.TurnRow{}, fmt.Errorf("service: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
		}
	}
	now := time.Now().UTC()
	turn.ID, turn.Model, turn.CreatedAt, turn.StartedAt = uuid.NewString(), model.Clone(), now, now
	t, turn, message, err := sv.cfg.DB.SubmitTurn(t.ID, turn)
	if err != nil {
		return db.TurnRow{}, err
	}
	defer sv.o.Wake(t.ID)
	if message != nil {
		sv.ev.TurnStarted(t, turn)
		sv.ev.Item(t, turn, *message, true)
		sv.ev.Status(t.ID)
	} else {
		slog.Info("turn queued", "thread", t.ID, "turn", turn.ID)
		sv.ev.Emit(event.Event{Kind: event.TurnQueued, Thread: t, Turn: turn})
	}
	return turn, nil
}

func (sv *Service) steer(threadID, expectedTurnID string, in Input, atts []db.Attachment) (db.TurnRow, error) {
	text, err := checkMessage(in.Text, len(in.Attachments))
	if err != nil {
		return db.TurnRow{}, err
	}
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return db.TurnRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	turn, active := st.ActiveTurn()
	if !active {
		return db.TurnRow{}, orchestrator.ErrNoActiveTurn
	}
	if turn.ID != expectedTurnID {
		return db.TurnRow{}, fmt.Errorf("%w (expected %s, in progress %s)", orchestrator.ErrTurnMismatch, expectedTurnID, turn.ID)
	}
	if err := sv.checkLength(turn.Model, text); err != nil {
		return db.TurnRow{}, err
	}
	if err := sv.checkImages(threadID, turn.Model, atts); err != nil {
		return db.TurnRow{}, err
	}
	stored, closed, err := sv.cfg.DB.Steer(threadID, db.NewUserMessage(uuid.NewString(), turn.ID, text, atts, time.Now().UTC()))
	if err != nil {
		return db.TurnRow{}, err
	}
	defer sv.o.Wake(threadID)
	sv.ev.Item(st.Thread, turn, stored, true)
	for _, it := range closed {
		sv.ev.Item(st.Thread, turn, it, false)
	}
	if len(closed) > 0 {
		slog.Info("turn steered out of its wait", "thread", threadID, "turn", turn.ID, "declined", len(closed))
		sv.ev.Status(threadID)
	}
	return turn, nil
}

func (sv *Service) resolveApproval(threadID, approvalID string, decision db.Decision, decidedBy string) error {
	stored, err := sv.respond(threadID, func(it db.ItemRow) bool { return it.WaitsForApproval() && it.Approval.ID == approvalID }, func(it db.ItemRow) (db.ItemRow, error) {
		if available := it.Approval.AvailableDecisions(); !slices.Contains(available, decision) {
			return db.ItemRow{}, fmt.Errorf("service: %w: %q is not one of %q", ErrInvalidDecision, decision, available)
		}
		now := time.Now().UTC()
		it.Approval.Decision, it.Approval.DecidedBy, it.Approval.DecidedAt = decision, decidedBy, &now
		return it, nil
	})
	if err != nil {
		return err
	}
	defer sv.o.Wake(threadID)
	// The rule is kept once the approval is: an approval refused, because the thread no longer waited on it, widens nothing.
	if decision == db.DecisionAcceptAlways {
		if err := sv.allowAlways(threadID, stored.Approval.Suggested); err != nil {
			return fmt.Errorf("service: the call was approved, but its rule was not kept: %w", err)
		}
	}
	return nil
}

func (sv *Service) allowAlways(threadID string, rules []string) error {
	t, err := sv.cfg.DB.ThreadRow(threadID)
	if err != nil {
		return fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	if parent := string(t.ParentThreadID); parent != "" {
		if t, err = sv.cfg.DB.ThreadRow(parent); err != nil {
			return fmt.Errorf("service: parent thread %s: %w", parent, err)
		}
	}
	allow := permission.Settings{Rules: permission.Rules{Allow: rules}}
	if t.WorkspaceID == "" {
		_, err := sv.UpdateThreadSettings(t.UserID, t.ID, permission.Resolve(t.Settings, allow))
		return err
	}
	w, err := sv.workspaceOf(t.WorkspaceID)
	if err != nil {
		return err
	}
	_, err = sv.UpdateWorkspaceSettings(t.UserID, w.ID, permission.Resolve(w.Settings, allow))
	return err
}

func (sv *Service) answerUserInput(threadID, itemID string, answers userinput.Answers) error {
	return sv.answer(threadID, func(it db.ItemRow) bool { return it.ID == itemID && !it.WaitsForApproval() }, func(it db.ItemRow) (db.ItemRow, error) {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			return db.ItemRow{}, err
		}
		if err := req.Check(answers, maxAnswerRunes); err != nil {
			return db.ItemRow{}, fmt.Errorf("service: %w: %v", ErrInvalidAnswer, err)
		}
		result, _, err := db.ResultOf(tools.Result{Output: answers})
		if err != nil {
			return db.ItemRow{}, err
		}
		now := time.Now().UTC()
		it.Result, it.Status, it.CompletedAt = result, db.ItemCompleted, &now
		return it, nil
	})
}

// respond records apply's answer to the request match finds. The thread's state is read before the answer is made,
// so the database refuses it with db.ErrNoSuchRequest if the thread no longer waits on that request.
func (sv *Service) respond(threadID string, match func(db.ItemRow) bool, apply func(db.ItemRow) (db.ItemRow, error)) (db.ItemRow, error) {
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return db.ItemRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	pending := st.PendingRequests()
	i := slices.IndexFunc(pending, match)
	if i < 0 {
		return db.ItemRow{}, db.ErrNoSuchRequest
	}
	activeTurn, _ := st.ActiveTurn()
	answered, err := apply(pending[i])
	if err != nil {
		return db.ItemRow{}, err
	}
	stored, err := sv.cfg.DB.AnswerRequest(threadID, answered)
	if err != nil {
		return db.ItemRow{}, err
	}
	sv.ev.Item(st.Thread, activeTurn, stored, false)
	sv.ev.Status(threadID)
	return stored, nil
}

// answer responds to a request and wakes the thread to go on with it.
func (sv *Service) answer(threadID string, match func(db.ItemRow) bool, apply func(db.ItemRow) (db.ItemRow, error)) error {
	if _, err := sv.respond(threadID, match, apply); err != nil {
		return err
	}
	sv.o.Wake(threadID)
	return nil
}

func checkMessage(text string, attachments int) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" && attachments == 0 {
		return "", fmt.Errorf("service: %w: empty message", ErrInvalidMessage)
	}
	return text, nil
}

// checkLength holds a message to the share of the model's input one message may take, so that it can always be sent.
func (sv *Service) checkLength(model llm.Model, text string) error {
	window, err := sv.res.Window(model)
	if err != nil {
		return err
	}
	if tokens, budget := llm.EstimateTokens(text), window.MessageBudget(); tokens > budget {
		return fmt.Errorf("service: %w: the message takes about %d tokens, and model %s takes at most %d in one message; send long text as a file attachment",
			ErrInvalidMessage, tokens, model.Name, budget)
	}
	return nil
}

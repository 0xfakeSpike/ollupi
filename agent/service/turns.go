package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
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
	return turn, nil
}

func (sv *Service) InterruptTurn(user, threadID, expectedTurnID string) error {
	if err := sv.owned(user, threadID); err != nil {
		return err
	}
	return sv.o.Interrupt(threadID, expectedTurnID)
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
	if queued, err := sv.cfg.DB.QueuedMessages(threadID); err != nil {
		return db.TurnRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	} else if queued >= maxQueuedMessages {
		return db.TurnRow{}, ErrQueueFull
	}
	model, err := sv.res.ConversationModel(st.Last, in.Connection)
	if err != nil {
		return db.TurnRow{}, fmt.Errorf("service: %w: %v", ErrInvalidMessage, err)
	}
	if err := sv.checkLength(model, text); err != nil {
		return db.TurnRow{}, err
	}
	if err := sv.checkImages(threadID, model, atts); err != nil {
		return db.TurnRow{}, err
	}
	main, err := sv.res.MainRole(model)
	if err != nil {
		return db.TurnRow{}, err
	}
	return sv.submit(st.Thread, db.TurnRow{CronJobID: in.CronJobID, Agent: string(main)}, model, text, atts)
}

// submit takes the message to the thread. A message that joins the turn waiting behind the current one tells of that
// turn again, as it is now, with the model and role of its latest message.
func (sv *Service) submit(t db.ThreadRow, turn db.TurnRow, model llm.Model, text string, atts []db.Attachment) (db.TurnRow, error) {
	t, turn, message, how, err := sv.record(t, turn, model, text, atts)
	if err != nil {
		return db.TurnRow{}, err
	}
	defer sv.o.Wake(t.ID)
	switch how {
	case db.Started:
		sv.ev.TurnStarted(t, turn)
	case db.Queued, db.Joined:
		slog.Info("turn queued", "thread", t.ID, "turn", turn.ID, "joined", how == db.Joined)
		sv.ev.Emit(event.Event{Kind: event.TurnQueued, Thread: t, Turn: turn})
	}
	sv.ev.Item(t, turn, message, true)
	sv.ev.Status(t.ID)
	return turn, nil
}

// record takes the message to the thread (db.SubmitTurn), unless the conversation's workspace cannot take it or is
// moving: the move lock is held while it is written, so that a move that starts sees it, and no longer.
func (sv *Service) record(t db.ThreadRow, turn db.TurnRow, model llm.Model, text string, atts []db.Attachment) (db.ThreadRow, db.TurnRow, db.ItemRow, db.Submission, error) {
	if t.WorkspaceID != "" {
		w, err := sv.workspaceOf(t.WorkspaceID)
		if err != nil {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, 0, err
		}
		if err := usable(w); err != nil {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, 0, err
		}
		sv.moveMu.Lock()
		defer sv.moveMu.Unlock()
		if sv.moving[w.ID] {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, 0, fmt.Errorf("service: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
		}
	}
	now := time.Now().UTC()
	turn.ID, turn.Model, turn.CreatedAt, turn.StartedAt = uuid.NewString(), model.Clone(), now, now
	return sv.cfg.DB.SubmitTurn(t.ID, turn, text, atts)
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

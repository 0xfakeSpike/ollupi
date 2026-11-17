package service

import (
	"context"
	"fmt"
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
}

func (sv *Service) StartTurn(ctx context.Context, user, threadID string, in Input) (db.TurnRow, db.Submission, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return db.TurnRow{}, "", err
	}
	atts, err := sv.attach(ctx, t, in.Attachments)
	if err != nil {
		return db.TurnRow{}, "", err
	}
	turn, how, err := sv.startTurn(threadID, in, atts, "")
	if err != nil {
		sv.removeAttachments(ctx, atts)
		return db.TurnRow{}, "", err
	}
	return turn, how, nil
}

// No caller asks for this one: the thread's owner scheduled it.
func (sv *Service) StartScheduledTurn(threadID, text, jobID string) error {
	_, _, err := sv.startTurn(threadID, Input{Text: text}, nil, jobID)
	return err
}

func (sv *Service) SteerTurn(ctx context.Context, user, threadID, expectedTurnID string, in Input) (db.TurnRow, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return db.TurnRow{}, err
	}
	atts, err := sv.attach(ctx, t, in.Attachments)
	if err != nil {
		return db.TurnRow{}, err
	}
	turn, err := sv.steer(threadID, expectedTurnID, in, atts)
	if err != nil {
		sv.removeAttachments(ctx, atts)
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

func (sv *Service) startTurn(threadID string, in Input, atts []db.Attachment, cronJobID string) (db.TurnRow, db.Submission, error) {
	text, err := checkMessage(in.Text, len(in.Attachments))
	if err != nil {
		return db.TurnRow{}, "", err
	}
	st, err := sv.cfg.DB.ThreadState(threadID)
	if err != nil {
		return db.TurnRow{}, "", fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	if queued, err := sv.cfg.DB.QueuedMessages(threadID); err != nil {
		return db.TurnRow{}, "", fmt.Errorf("service: thread %s: %w", threadID, err)
	} else if queued >= maxQueuedMessages {
		return db.TurnRow{}, "", ErrQueueFull
	}
	model, err := sv.res.ConversationModel(st.Last, in.Connection)
	if err != nil {
		return db.TurnRow{}, "", fmt.Errorf("service: %w: %v", ErrInvalidMessage, err)
	}
	if err := sv.checkLength(model, text); err != nil {
		return db.TurnRow{}, "", err
	}
	if err := sv.checkImages(threadID, model, atts); err != nil {
		return db.TurnRow{}, "", err
	}
	main, err := sv.res.MainRole(model)
	if err != nil {
		return db.TurnRow{}, "", err
	}
	t, turn, message, how, err := sv.record(st.Thread, db.TurnRow{CronJobID: cronJobID, Agent: string(main)}, model, text, atts)
	if err != nil {
		return db.TurnRow{}, "", err
	}
	if how == db.Started {
		sv.o.Started(t.ID)
		return turn, how, nil
	}
	defer sv.o.Wake(t.ID)
	sv.ev.Emit(event.Event{Kind: event.TurnQueued, Thread: t, Turn: turn})
	sv.ev.Item(t, turn, message, true)
	sv.ev.Status(t.ID)
	return turn, how, nil
}

// The move lock is held while the message is written, so that a move that starts sees it.
func (sv *Service) record(t db.ThreadRow, turn db.TurnRow, model llm.Model, text string, atts []db.Attachment) (db.ThreadRow, db.TurnRow, db.ItemRow, db.Submission, error) {
	if t.WorkspaceID != "" {
		w, err := sv.cfg.DB.WorkspaceOf(t.UserID, string(t.WorkspaceID))
		if err != nil {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, "", fmt.Errorf("service: workspace %s: %w", t.WorkspaceID, err)
		}
		if err := usable(w); err != nil {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, "", err
		}
		sv.moveMu.Lock()
		defer sv.moveMu.Unlock()
		if sv.moving[w.ID] {
			return db.ThreadRow{}, db.TurnRow{}, db.ItemRow{}, "", fmt.Errorf("service: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
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

// A message within the budget can always be sent.
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

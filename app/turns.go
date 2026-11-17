package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

const (
	maxQueued      = 20
	maxAnswerRunes = 4000
	messagePercent = 25
)

type Input struct {
	Text        string
	Attachments []AttachmentInput
	Connection  string
}

func (a *App) Send(ctx context.Context, u, id string, in Input) (turn conversation.Turn, inProgress bool, err error) {
	atts, err := a.attach(ctx, u, id, in.Attachments)
	if err != nil {
		return conversation.Turn{}, false, err
	}
	err = a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, id)
		if err != nil {
			return err
		}
		turn, inProgress, err = a.queue(tx, th, in.Text, atts, in.Connection, "")
		return err
	})
	if err != nil {
		a.detach(ctx, atts)
	}
	return turn, inProgress, err
}

func (a *App) Scheduled(tx store.Tx, id, prompt, job string) error {
	th, err := tx.Thread(id)
	if err != nil {
		return err
	}
	_, _, err = a.queue(tx, th, prompt, nil, "", job)
	return err
}

func (a *App) queue(tx store.Tx, th store.Thread, text string, atts []conversation.Attachment, connection, job string) (conversation.Turn, bool, error) {
	text, err := message(text, len(atts))
	if err != nil {
		return conversation.Turn{}, false, err
	}
	if th.Archived != nil {
		return conversation.Turn{}, false, ErrThreadArchived
	}
	if err := a.usableWorkspace(tx, th); err != nil {
		return conversation.Turn{}, false, err
	}
	v, err := tx.View(th.ID)
	if err != nil {
		return conversation.Turn{}, false, err
	}
	if len(v.Behind()) >= maxQueued {
		return conversation.Turn{}, false, ErrQueueFull
	}
	var last *llm.Model
	if n := len(v.Turns); n > 0 {
		last = &v.Turns[n-1].Model
	}
	model, err := a.model(connection, last)
	if err != nil {
		return conversation.Turn{}, false, err
	}
	limits, err := a.fits(model, v, text, atts)
	if err != nil {
		return conversation.Turn{}, false, err
	}
	id := uuid.NewString()
	v, err = tx.Append(th.ID,
		conversation.Entry{Turn: id, Body: conversation.TurnQueued{Model: model, Role: string(agent.Main(limits.Input())), CronJob: job}},
		conversation.Entry{Turn: id, Item: uuid.NewString(), Body: conversation.UserMessage{Text: text, Attachments: atts}})
	if err != nil {
		return conversation.Turn{}, false, err
	}
	turn, _ := v.TurnOf(id)
	active, _ := v.Active()
	return turn, active.ID == id, nil
}

func (a *App) Steer(ctx context.Context, u, id, expected string, in Input) (conversation.Turn, error) {
	atts, err := a.attach(ctx, u, id, in.Attachments)
	if err != nil {
		return conversation.Turn{}, err
	}
	var turn conversation.Turn
	err = a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, id)
		if err != nil {
			return err
		}
		v, err := tx.View(th.ID)
		if err != nil {
			return err
		}
		if turn, err = activeTurn(v, expected); err != nil {
			return err
		}
		text, err := message(in.Text, len(atts))
		if err != nil {
			return err
		}
		if _, err := a.fits(turn.Model, v, text, atts); err != nil {
			return err
		}
		var entries []conversation.Entry
		for _, it := range v.Requests() {
			body := conversation.Body(conversation.Answered{Skipped: true})
			if it.Approval != nil {
				body = conversation.Decided{Decision: conversation.Decline, By: u}
			}
			entries = append(entries, conversation.Entry{Turn: turn.ID, Item: it.ID, Body: body})
		}
		entries = append(entries, conversation.Entry{Turn: turn.ID, Item: uuid.NewString(), Body: conversation.UserMessage{Text: text, Attachments: atts}})
		_, err = tx.Append(th.ID, entries...)
		return err
	})
	if err != nil {
		a.detach(ctx, atts)
	}
	return turn, err
}

func (a *App) Interrupt(ctx context.Context, u, id, expected string) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		if _, err := thread(tx, u, id); err != nil {
			return err
		}
		v, err := tx.View(id)
		if err != nil {
			return err
		}
		if _, err := activeTurn(v, expected); err != nil {
			return err
		}
		return run.End(tx, id, conversation.Interrupted, "interrupted by the user")
	})
}

func activeTurn(v conversation.View, expected string) (conversation.Turn, error) {
	turn, ok := v.Active()
	if !ok {
		return conversation.Turn{}, ErrNoActiveTurn
	}
	if turn.ID != expected {
		return conversation.Turn{}, fmt.Errorf("%w (named %s, in progress %s)", ErrTurnMismatch, expected, turn.ID)
	}
	return turn, nil
}

func message(text string, attachments int) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" && attachments == 0 {
		return "", fmt.Errorf("%w: empty message", ErrInvalidMessage)
	}
	return text, nil
}

func (a *App) fits(model llm.Model, v conversation.View, text string, atts []conversation.Attachment) (llm.Limits, error) {
	limits, err := a.models.Limits(model)
	if err != nil {
		return llm.Limits{}, err
	}
	if tokens, budget := llm.EstimateTokens(text), max(256, limits.Input()*messagePercent/100); tokens > budget {
		return llm.Limits{}, fmt.Errorf("%w: the message takes about %d tokens, and model %s takes at most %d in one message; send long text as a file attachment",
			ErrInvalidMessage, tokens, model.Name, budget)
	}
	if model.Images {
		return limits, nil
	}
	if slices.ContainsFunc(atts, isImage) {
		return llm.Limits{}, fmt.Errorf("%w: model %s does not accept images; pick a model that does", ErrInvalidMessage, model.Name)
	}
	for _, it := range v.Items {
		if slices.ContainsFunc(it.Attachments, isImage) {
			return llm.Limits{}, fmt.Errorf("%w: this conversation shows the model images, and model %s does not accept them; pick a model that does", ErrInvalidMessage, model.Name)
		}
	}
	return limits, nil
}

func isImage(att conversation.Attachment) bool { return att.Kind == imageKind }

func (a *App) usableWorkspace(tx store.Tx, th store.Thread) error {
	if th.Workspace == "" {
		return nil
	}
	w, err := tx.Workspace(th.User, th.Workspace)
	if err != nil {
		return err
	}
	if err := w.Usable(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.moving[w.ID] {
		return fmt.Errorf("workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
	}
	return nil
}

type Request struct {
	Thread store.Thread
	Item   conversation.Item
}

func (a *App) Requests(ctx context.Context, u, id string) ([]Request, error) {
	var out []Request
	err := a.st.Read(ctx, func(r store.Reader) error {
		var threads []store.Thread
		if id == "" {
			ids, err := r.Waiting(u)
			if err != nil {
				return err
			}
			for _, w := range ids {
				th, err := r.Thread(w)
				if err != nil {
					return err
				}
				threads = append(threads, th)
			}
		} else {
			th, err := thread(r, u, id)
			if err != nil {
				return err
			}
			children, err := r.Children(id)
			if err != nil {
				return err
			}
			threads = append([]store.Thread{th}, children...)
		}
		for _, t := range threads {
			v, err := r.View(t.ID)
			if err != nil {
				return err
			}
			for _, it := range v.Requests() {
				out = append(out, Request{Thread: t, Item: it})
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return askedAt(out[i].Item).Before(askedAt(out[j].Item)) })
	return out, err
}

func askedAt(it conversation.Item) time.Time {
	if it.Approval != nil {
		return it.Approval.AskedAt
	}
	return it.Input.AskedAt
}

func (a *App) Decide(ctx context.Context, u, id, approval string, decision conversation.Decision) error {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return err
	}
	return a.st.Update(ctx, func(tx store.Tx) error {
		if _, err := thread(tx, u, id); err != nil {
			return err
		}
		v, err := tx.View(id)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(v.Requests(), func(it conversation.Item) bool { return it.Approval != nil && it.Approval.Approval == approval })
		if i < 0 {
			return ErrNoSuchRequest
		}
		it := v.Requests()[i]
		if available := it.Approval.Decisions(); !slices.Contains(available, decision) {
			return fmt.Errorf("%w: %q is not one of %q", ErrInvalidDecision, decision, available)
		}
		if _, err := tx.Append(id, conversation.Entry{Turn: it.Turn, Item: it.ID, Body: conversation.Decided{Decision: decision, By: u}}); err != nil {
			return err
		}
		if decision != conversation.AcceptAlways {
			return nil
		}
		return a.allowAlways(tx, box, id, it.Approval.Suggested)
	})
}

func (a *App) Answer(ctx context.Context, u, id, item string, answers ask.Answers) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		if _, err := thread(tx, u, id); err != nil {
			return err
		}
		v, err := tx.View(id)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(v.Requests(), func(it conversation.Item) bool { return it.ID == item && it.Input != nil })
		if i < 0 {
			return ErrNoSuchRequest
		}
		it := v.Requests()[i]
		answered := conversation.Answered{Skipped: true}
		if answers != nil {
			req, err := ask.Decode(it.Input.Request)
			if err != nil {
				return err
			}
			if err := req.Check(answers, maxAnswerRunes); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
			}
			answered = conversation.Answered{Output: mustJSON(answers)}
		}
		_, err = tx.Append(id, conversation.Entry{Turn: it.Turn, Item: it.ID, Body: answered})
		return err
	})
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

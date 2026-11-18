package extract

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
)

const (
	extractionTokens    = 12000
	minExtractionTokens = 8192
	callTimeout         = 2 * time.Minute
	skipTurns           = 256
)

type Extractor struct {
	st     store.Store
	model  llm.Model
	client func(user string) (llm.Client, error)
	budget int

	wake  chan struct{}
	mu    sync.Mutex
	dirty map[string]bool
}

func New(st store.Store, model llm.Model, limits llm.Limits, client func(user string) (llm.Client, error)) (*Extractor, error) {
	budget := min(extractionTokens, limits.Input())
	if budget < minExtractionTokens {
		return nil, fmt.Errorf("extract: the extraction budget is %d tokens; it must be at least %d so that one piece of evidence always fits", budget, minExtractionTokens)
	}
	return &Extractor{st: st, model: model, client: client, budget: budget, wake: make(chan struct{}, 1), dirty: map[string]bool{}}, nil
}

func (x *Extractor) Run(ctx context.Context) error {
	sub := x.st.Feed().SubscribeAll(x.hear)
	defer sub.Close()
	var users []string
	if err := x.st.Read(ctx, func(r store.Reader) (err error) { users, err = r.Users(); return err }); err != nil {
		return err
	}
	x.mark(users...)
	retry := map[string]time.Time{}
	for {
		for _, u := range x.take() {
			delete(retry, u)
			due, err := x.readAll(ctx, u)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return err
			}
			if !due.IsZero() {
				retry[u] = due
			}
		}
		var timer <-chan time.Time
		if next, ok := earliest(retry); ok {
			timer = time.After(time.Until(next))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-x.wake:
		case <-timer:
			now := time.Now()
			for u, due := range retry {
				if !now.Before(due) {
					x.mark(u)
				}
			}
		}
	}
}

func (x *Extractor) hear(e store.Event) {
	if e.Change == nil {
		return
	}
	for _, entry := range e.Change.Entries {
		if _, ended := entry.Body.(conversation.TurnEnded); ended {
			x.mark(e.User)
			return
		}
	}
}

func (x *Extractor) mark(users ...string) {
	x.mu.Lock()
	for _, u := range users {
		x.dirty[u] = true
	}
	x.mu.Unlock()
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

func (x *Extractor) take() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	users := make([]string, 0, len(x.dirty))
	for u := range x.dirty {
		users = append(users, u)
	}
	clear(x.dirty)
	return users
}

func earliest(retry map[string]time.Time) (time.Time, bool) {
	var next time.Time
	for _, due := range retry {
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next, !next.IsZero()
}

func (x *Extractor) readAll(ctx context.Context, u string) (time.Time, error) {
	for {
		var state memory.State
		var settings user.Settings
		var ended []memory.Cursor
		err := x.st.Read(ctx, func(r store.Reader) (err error) {
			if state, err = r.MemoryState(u); err != nil {
				return err
			}
			if settings, err = r.Settings(u); err != nil {
				return err
			}
			ended, err = r.Ended(u, state.Cursor, skipTurns)
			return err
		})
		switch {
		case err != nil:
			return time.Time{}, err
		case len(ended) == 0:
			return time.Time{}, nil
		case !state.Due(time.Now()):
			return *state.NextAttempt, nil
		}
		if !settings.Memory {
			state.Cursor = ended[len(ended)-1]
			if err := x.st.Update(ctx, func(tx store.Tx) error { return tx.SetMemoryState(u, state) }); err != nil {
				return time.Time{}, err
			}
			continue
		}
		turn := ended[0]
		learned, err := x.learn(ctx, u, settings, turn)
		if ctx.Err() != nil {
			return time.Time{}, ctx.Err()
		}
		now := time.Now().UTC()
		var f failure
		switch {
		case errors.As(err, &f):
			state = state.Failed(turn, memory.Failure{Code: f.code, Error: f.Error()}, now)
		case err != nil:
			return time.Time{}, err
		default:
			state = state.Read(turn, now)
		}
		if err := x.commit(ctx, u, learned, state); err != nil {
			return time.Time{}, err
		}
	}
}

type failure struct {
	code memory.FailureCode
	err  error
}

func (f failure) Error() string { return f.err.Error() }
func (f failure) Unwrap() error { return f.err }

type learned struct {
	places  []memory.Place
	changes []memory.Memory
	read    map[string]time.Time
}

func (x *Extractor) learn(ctx context.Context, u string, settings user.Settings, turn memory.Cursor) (learned, error) {
	b, places, err := x.batch(ctx, u, turn)
	if errors.Is(err, store.ErrNotFound) {
		return learned{}, nil
	}
	if err != nil {
		return learned{}, err
	}
	l := learned{places: places, read: map[string]time.Time{}}
	for _, m := range b.Existing {
		l.read[m.ID] = m.Updated
	}
	parts := memory.Pack(b, x.budget)
	if len(parts) == 0 {
		return l, nil
	}
	client, err := x.client(u)
	if err != nil {
		return learned{}, failure{memory.ModelRequest, fmt.Errorf("no model client for the user: %w", err)}
	}
	redactor := redact.New(settings.RedactionKey)
	current := b.Existing
	for _, part := range parts {
		part.Existing = current
		payload, err := memory.Input(part, x.budget)
		if err != nil {
			return learned{}, failure{memory.BadEvidence, err}
		}
		if settings.Redaction {
			payload = redactor.Mask(payload)
		}
		content, err := x.ask(ctx, client, payload)
		if err != nil {
			return learned{}, err
		}
		ops, err := memory.Parse(content, part, redactor.Restore)
		if err != nil {
			return learned{}, failure{memory.InvalidResponse, err}
		}
		for _, m := range memory.Apply(part, ops, time.Now().UTC(), uuid.NewString) {
			current = replace(current, m)
			l.changes = replace(l.changes, m)
		}
	}
	return l, nil
}

func (x *Extractor) batch(ctx context.Context, u string, turn memory.Cursor) (b memory.Batch, places []memory.Place, err error) {
	err = x.st.Read(ctx, func(r store.Reader) error {
		th, err := r.Thread(turn.Thread)
		if err != nil {
			return err
		}
		v, err := r.View(turn.Thread)
		if err != nil {
			return err
		}
		places = []memory.Place{memory.GlobalPlace, memory.Local(th.Workspace)}
		existing, err := r.Memories(u, places...)
		if err != nil {
			return err
		}
		b = memory.Gather(u, turn.Thread, th.Workspace, v, turn.Turn, existing)
		return nil
	})
	return b, places, err
}

func replace(memories []memory.Memory, m memory.Memory) []memory.Memory {
	for i := range memories {
		if memories[i].ID == m.ID {
			memories[i] = m
			return memories
		}
	}
	return append(memories, m)
}

func (x *Extractor) ask(ctx context.Context, client llm.Client, payload string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := client.Chat(ctx, llm.ChatRequest{
		Model:       x.model,
		Messages:    []llm.ChatMessage{{Role: llm.RoleSystem, Content: memory.Instructions}, {Role: llm.RoleUser, Content: payload}},
		ToolChoice:  llm.ToolChoiceNone,
		InputTokens: llm.EstimateTokens(memory.Instructions) + llm.EstimateTokens(payload),
	}, nil)
	if err != nil {
		return "", failure{memory.ModelRequest, fmt.Errorf("the extraction model request failed: %w", err)}
	}
	if len(resp.Message.ToolCalls) != 0 || (resp.FinishReason != "" && resp.FinishReason != "stop") {
		return "", failure{memory.InvalidResponse, errors.New("the extraction did not produce a complete text response")}
	}
	return resp.Message.Content, nil
}

func (x *Extractor) commit(ctx context.Context, u string, l learned, state memory.State) error {
	return x.st.Update(ctx, func(tx store.Tx) error {
		if len(l.changes) > 0 {
			now, err := tx.Memories(u, l.places...)
			if err != nil {
				return err
			}
			byID := map[string]memory.Memory{}
			taken := map[memory.Place]map[string]bool{}
			for _, m := range now {
				byID[m.ID] = m
				if taken[m.Place] == nil {
					taken[m.Place] = map[string]bool{}
				}
				taken[m.Place][m.Topic] = true
			}
			for _, m := range l.changes {
				before, wasRead := l.read[m.ID]
				stored, kept := byID[m.ID]
				changedMeanwhile := wasRead && (!kept || !stored.Updated.Equal(before))
				topicTaken := !wasRead && taken[m.Place][m.Topic]
				if changedMeanwhile || topicTaken {
					continue
				}
				if err := tx.PutMemory(m); err != nil {
					return err
				}
			}
		}
		return tx.SetMemoryState(u, state)
	})
}

package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

type Options struct {
	Model    llm.Model
	Generate bool
	Use      bool
	// The model passed to New checks the limits; without ClientFor users share that model.
	ClientFor func(user string) (llm.Client, error)
}

const (
	extractInterval     = time.Minute
	extractTimeout      = 2 * time.Minute
	extractionTokens    = 12000
	minExtractionTokens = 8192
	batchSize           = 24
)

type Manager struct {
	db     *db.DB
	model  llm.Client
	opts   Options
	budget int
}

func New(store *db.DB, model llm.Client, opts Options) (*Manager, error) {
	if store == nil {
		return nil, errors.New("memory: database is required")
	}
	if opts.Generate && (model == nil || strings.TrimSpace(opts.Model.Name) == "") {
		return nil, errors.New("memory: extraction model is required")
	}
	m := &Manager{db: store, model: model, opts: opts, budget: extractionTokens}
	if opts.Generate {
		limits, err := model.Limits(opts.Model.Name)
		if err != nil {
			return nil, fmt.Errorf("memory: extraction model: %w", err)
		}
		m.budget = min(m.budget, limits.Input())
		if m.budget < minExtractionTokens {
			return nil, fmt.Errorf("memory: the extraction budget is %d tokens; it must be at least %d so that one piece of evidence always fits", m.budget, minExtractionTokens)
		}
	}
	return m, nil
}

func (m *Manager) Run(ctx context.Context) {
	if !m.opts.Generate {
		return
	}
	timer := time.NewTicker(extractInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.RunOnce(ctx) // a run that fails is tried again at the next tick
		}
	}
}

// A user whose extraction fails does not hold up the others.
func (m *Manager) RunOnce(ctx context.Context) error {
	if !m.opts.Generate {
		return nil
	}
	users, err := m.db.Users()
	if err != nil {
		return failure{db.MemoryStorage, fmt.Errorf("could not list the users: %w", err)}
	}
	for _, user := range users {
		err := m.runFor(ctx, user)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			continue
		}
		m.db.SetMemoryFailure(user, failureOf(err))
	}
	return nil
}

func (m *Manager) runFor(ctx context.Context, user string) (err error) {
	var batch *Batch
	defer func() {
		if p := recover(); p != nil {
			err = failure{db.MemoryEvidence, errors.New("memory extraction crashed")}
			// A batch that crashes is put off, and in the end skipped, as one that fails: it would crash every time.
			if batch != nil {
				err = m.deferBatch(batch, err)
			}
		}
	}()
	state, err := m.db.MemoryState(user)
	if err != nil {
		return failure{db.MemoryStorage, fmt.Errorf("could not read memory state: %w", err)}
	}
	if state.NextAttempt != nil && time.Now().Before(*state.NextAttempt) {
		return nil
	}
	var payload string
	var estimated int
	batch, payload, estimated, err = m.gatherEvidence(user)
	if batch == nil {
		return err
	}
	if err != nil {
		return m.deferBatch(batch, err)
	}
	report := db.MemoryReport{Model: m.opts.Model.Clone(), EstimatedInputTokens: estimated}
	var ops []Operation
	if len(batch.Facts) > 0 {
		if ops, err = m.extract(ctx, batch, payload, &report); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return m.deferBatch(batch, err)
		}
	}
	if err := commit(m.db, batch, ops, report); err != nil {
		return m.deferBatch(batch, failure{db.MemoryStorage, fmt.Errorf("could not commit the memory batch of turn %s: %w", batch.TurnID, err)})
	}
	return nil
}

type failure struct {
	code db.MemoryFailureCode
	err  error
}

func (f failure) Error() string { return f.err.Error() }
func (f failure) Unwrap() error { return f.err }

func failureOf(err error) db.MemoryFailure {
	f := failure{code: db.MemoryStorage}
	errors.As(err, &f)
	return db.MemoryFailure{Code: f.code, Error: clipMemory(err.Error(), 1000)}
}

func (m *Manager) gatherEvidence(user string) (*Batch, string, int, error) {
	for limit := batchSize; ; limit = max(1, limit/2) {
		batch, err := nextBatch(m.db, user, limit)
		if err != nil {
			return nil, "", 0, failure{db.MemoryEvidence, fmt.Errorf("could not read pending memory evidence: %w", err)}
		}
		if batch == nil {
			return nil, "", 0, nil
		}
		payload, estimated, err := extractionInput(batch, m.budget)
		if err == nil {
			return batch, payload, estimated, nil
		}
		if limit == 1 {
			return batch, "", 0, failure{db.MemoryEvidence, err}
		}
	}
}

func (m *Manager) deferBatch(b *Batch, cause error) error {
	skip := db.MemorySkip{Entries: b.Entries, ThreadID: b.ThreadID, TurnID: b.TurnID, MemoryFailure: failureOf(cause)}
	if _, err := m.db.DeferMemory(b.UserID, b.Cursor, skip, time.Now()); err != nil {
		return fmt.Errorf("could not defer the memory batch of turn %s (%v): %w", b.TurnID, cause, err)
	}
	return nil
}

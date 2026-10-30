package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo"
)

type Options struct {
	Model    llm.Model
	Generate bool
	Use      bool
}

const (
	extractInterval     = time.Minute
	extractTimeout      = 2 * time.Minute
	extractionTokens    = 12000
	minExtractionTokens = 8192
	batchSize           = 24
)

type Manager struct {
	db     *repo.DB
	model  llm.Client
	opts   Options
	budget int
}

func New(db *repo.DB, model llm.Client, opts Options) (*Manager, error) {
	if db == nil {
		return nil, errors.New("memory: database is required")
	}
	if opts.Generate && (model == nil || strings.TrimSpace(opts.Model.Name) == "") {
		return nil, errors.New("memory: extraction model is required")
	}
	m := &Manager{db: db, model: model, opts: opts, budget: extractionTokens}
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
			err := m.RunOnce(ctx)
			if err == nil || ctx.Err() != nil {
				continue
			}
			log.Printf("memory: %v", err)
			if rerr := m.db.SetMemoryFailure(failureOf(err)); rerr != nil {
				log.Printf("memory: the error could not be recorded: %v", rerr)
			}
		}
	}
}

func (m *Manager) RunOnce(ctx context.Context) error {
	if !m.opts.Generate {
		return nil
	}
	state, err := m.db.MemoryState()
	if err != nil {
		return failure{repo.MemoryStorage, fmt.Errorf("could not read memory state: %w", err)}
	}
	if state.NextAttempt != nil && time.Now().Before(*state.NextAttempt) {
		return nil
	}
	batch, payload, estimated, err := m.gatherEvidence()
	if batch == nil {
		return err
	}
	if err != nil {
		return m.deferBatch(batch, err)
	}
	report := repo.MemoryReport{Model: m.opts.Model.Clone(), EstimatedInputTokens: estimated}
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
		return m.deferBatch(batch, failure{repo.MemoryStorage, fmt.Errorf("could not commit the memory batch of turn %s: %w", batch.TurnID, err)})
	}
	return nil
}

type failure struct {
	code repo.MemoryFailureCode
	err  error
}

func (f failure) Error() string { return f.err.Error() }
func (f failure) Unwrap() error { return f.err }

func failureOf(err error) repo.MemoryFailure {
	f := failure{code: repo.MemoryStorage}
	errors.As(err, &f)
	return repo.MemoryFailure{Code: f.code, Error: clipMemory(err.Error(), 1000)}
}

func (m *Manager) gatherEvidence() (*Batch, string, int, error) {
	for limit := batchSize; ; limit = max(1, limit/2) {
		batch, err := nextBatch(m.db, limit)
		if err != nil {
			return nil, "", 0, failure{repo.MemoryEvidence, fmt.Errorf("could not read pending memory evidence: %w", err)}
		}
		if batch == nil {
			return nil, "", 0, nil
		}
		payload, estimated, err := extractionInput(batch, m.budget)
		if err == nil {
			return batch, payload, estimated, nil
		}
		if limit == 1 {
			return batch, "", 0, failure{repo.MemoryEvidence, err}
		}
	}
}

func (m *Manager) extract(ctx context.Context, batch *Batch, payload string, report *repo.MemoryReport) ([]Operation, error) {
	callCtx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()
	settings, err := m.db.Settings()
	if err != nil {
		return nil, failure{repo.MemoryStorage, fmt.Errorf("could not read the redaction setting: %w", err)}
	}
	redactor := redact.New(settings.RedactionKey)
	if settings.Redaction {
		payload = redactor.Mask(payload)
	}
	response, err := m.model.Chat(callCtx, llm.ChatRequest{
		Model:      m.opts.Model,
		Messages:   []llm.ChatMessage{{Role: llm.RoleSystem, Content: extractionInstructions}, {Role: llm.RoleUser, Content: payload}},
		ToolChoice: llm.ToolChoiceNone,
	}, nil)
	if err != nil {
		return nil, failure{repo.MemoryModelRequest, fmt.Errorf("memory extraction model request failed: %w", err)}
	}
	if len(response.Message.ToolCalls) != 0 || (response.FinishReason != "" && response.FinishReason != "stop") {
		return nil, failure{repo.MemoryInvalidResponse, errors.New("memory extraction did not produce a complete text response")}
	}
	ops, err := parseOperations(response.Message.Content, batch, redactor.Restore)
	if err != nil {
		return nil, failure{repo.MemoryInvalidResponse, err}
	}
	report.InputTokens, report.OutputTokens = response.Usage.PromptTokens, response.Usage.CompletionTokens
	report.UsageReported = response.UsageReported
	return ops, nil
}

func (m *Manager) deferBatch(b *Batch, cause error) error {
	skip := repo.MemorySkip{Entries: b.Entries, ThreadID: b.ThreadID, TurnID: b.TurnID, MemoryFailure: failureOf(cause)}
	skipped, err := m.db.DeferMemory(b.Cursor, skip, time.Now())
	if err != nil {
		return fmt.Errorf("could not defer the memory batch of turn %s (%v): %w", b.TurnID, cause, err)
	}
	if skipped {
		log.Printf("memory: turn %s skipped up to item %d after repeated failures: %v", b.TurnID, b.Cursor.Seq, cause)
	} else {
		log.Printf("memory: turn %s deferred: %v", b.TurnID, cause)
	}
	return nil
}

var ErrInvalidChange = errors.New("invalid memory change")

func (m *Manager) List(scope repo.MemoryScope, workspaceID string) ([]repo.MemoryRow, error) {
	if err := checkPlace(scope, workspaceID); err != nil {
		return nil, err
	}
	entries, err := m.db.Memories(scope, workspaceID, false)
	if entries == nil {
		entries = []repo.MemoryRow{}
	}
	return entries, err
}

func checkPlace(scope repo.MemoryScope, workspaceID string) error {
	if !slices.Contains(repo.MemoryScopeValues, scope) {
		return fmt.Errorf("%w: the scope must be one of %v", ErrInvalidChange, repo.MemoryScopeValues)
	}
	if (scope == repo.MemoryWorkspace) != (workspaceID != "") {
		return fmt.Errorf("%w: give a workspaceId with the workspace scope, and only with it", ErrInvalidChange)
	}
	return nil
}

func (m *Manager) Move(id string) (repo.MemoryRow, error) {
	var out repo.MemoryRow
	err := m.db.Tx(func(tx *repo.DB) error {
		e, err := tx.Memory(id)
		if err != nil {
			return err
		}
		if e.Status == repo.MemoryForgotten {
			return fmt.Errorf("%w: a forgotten memory cannot be moved", ErrInvalidChange)
		}
		if e.Scope == repo.MemoryGlobal {
			e.Scope, e.WorkspaceID, e.OriginWorkspaceID = localScope(e.OriginWorkspaceID), e.OriginWorkspaceID, ""
		} else {
			e.Scope, e.WorkspaceID, e.OriginWorkspaceID = repo.MemoryGlobal, "", e.WorkspaceID
		}
		there, err := tx.Memories(e.Scope, e.WorkspaceID, false)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(there, func(o repo.MemoryRow) bool { return o.Topic == e.Topic }) {
			return fmt.Errorf("%w: a memory with the topic %q is already there; forget or edit that one instead", ErrInvalidChange, e.Topic)
		}
		e.UpdatedAt = time.Now().UTC()
		out = e
		return tx.SaveMemory(e)
	})
	return out, err
}

func (m *Manager) Status() (repo.MemoryStatus, error) {
	s, err := m.db.MemoryStatus()
	s.GenerationEnabled, s.UseEnabled = m.opts.Generate, m.opts.Use
	return s, err
}

func (m *Manager) Edit(id, text string) (repo.MemoryRow, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxTextRunes {
		return repo.MemoryRow{}, fmt.Errorf("%w: memory text must contain 1 to %d characters", ErrInvalidChange, maxTextRunes)
	}
	var out repo.MemoryRow
	err := m.db.Tx(func(tx *repo.DB) error {
		e, err := tx.Memory(id)
		if err != nil {
			return err
		}
		if e.Status == repo.MemoryForgotten {
			return fmt.Errorf("%w: a forgotten memory cannot be edited", ErrInvalidChange)
		}
		e.Text, e.UserEdited, e.Status = text, true, repo.MemoryActive
		e.UpdatedAt = time.Now().UTC()
		out = e
		return tx.SaveMemory(e)
	})
	return out, err
}

func (m *Manager) Forget(id string) error {
	return m.db.Tx(func(tx *repo.DB) error {
		e, err := tx.Memory(id)
		if err != nil {
			return err
		}
		if e.Status == repo.MemoryForgotten {
			return nil
		}
		e.Status, e.Text = repo.MemoryForgotten, ""
		e.UpdatedAt = time.Now().UTC()
		return tx.SaveMemory(e)
	})
}

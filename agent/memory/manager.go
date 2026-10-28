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
	Model            llm.Model
	Generate         bool
	Use              bool
	Interval         time.Duration
	Timeout          time.Duration
	ExtractionTokens int
	BatchSize        int
}

const (
	defaultExtractionTokens = 12000
	minExtractionTokens     = 8192
)

type Manager struct {
	db     *repo.DB
	model  *llm.OpenAI
	opts   Options
	budget int
}

func New(db *repo.DB, model *llm.OpenAI, opts Options) (*Manager, error) {
	if db == nil {
		return nil, errors.New("memory: database is required")
	}
	if opts.Generate && (model == nil || strings.TrimSpace(opts.Model.Name) == "") {
		return nil, errors.New("memory: extraction model is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.ExtractionTokens <= 0 {
		opts.ExtractionTokens = defaultExtractionTokens
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 24
	}
	m := &Manager{db: db, model: model, opts: opts, budget: opts.ExtractionTokens}
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
	timer := time.NewTicker(m.opts.Interval)
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
			if rerr := m.db.SetMemoryError(clipMemory(err.Error(), 1000)); rerr != nil {
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
		return fmt.Errorf("could not read memory state: %w", err)
	}
	if state.NextAttempt != nil && time.Now().Before(*state.NextAttempt) {
		return nil
	}
	batch, payload, estimated, err := m.gatherEvidence()
	if err != nil {
		return err
	}
	if batch == nil {
		return nil
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
		return fmt.Errorf("could not commit the memory batch of turn %s: %w", batch.TurnID, err)
	}
	return nil
}

func (m *Manager) gatherEvidence() (*Batch, string, int, error) {
	for limit := m.opts.BatchSize; ; limit = max(1, limit/2) {
		batch, err := nextBatch(m.db, limit)
		if err != nil {
			return nil, "", 0, fmt.Errorf("could not read pending memory evidence: %w", err)
		}
		if batch == nil {
			return nil, "", 0, nil
		}
		payload, estimated, err := extractionInput(batch, m.budget)
		if err == nil {
			return batch, payload, estimated, nil
		}
		if limit == 1 {
			return nil, "", 0, err
		}
	}
}

func (m *Manager) extract(ctx context.Context, batch *Batch, payload string, report *repo.MemoryReport) ([]Operation, error) {
	callCtx, cancel := context.WithTimeout(ctx, m.opts.Timeout)
	defer cancel()
	settings, err := m.db.Settings()
	if err != nil {
		return nil, fmt.Errorf("could not read the redaction setting: %w", err)
	}
	redactor := redact.New()
	if settings.Redaction {
		payload = redactor.Mask(payload)
	}
	response, err := m.model.Chat(callCtx, llm.ChatRequest{
		Model:      m.opts.Model,
		Messages:   []llm.ChatMessage{{Role: llm.RoleSystem, Content: extractionInstructions}, {Role: llm.RoleUser, Content: payload}},
		ToolChoice: llm.ToolChoiceNone,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("memory extraction model request failed: %w", err)
	}
	if len(response.Message.ToolCalls) != 0 || (response.FinishReason != "" && response.FinishReason != "stop") {
		return nil, errors.New("memory extraction did not produce a complete text response")
	}
	ops, err := parseOperations(response.Message.Content, batch, redactor.Restore)
	if err != nil {
		return nil, err
	}
	report.InputTokens, report.OutputTokens = response.Usage.PromptTokens, response.Usage.CompletionTokens
	report.UsageReported = response.UsageReported
	return ops, nil
}

func (m *Manager) deferBatch(b *Batch, cause error) error {
	skipped, err := m.db.DeferMemory(b.Cursor, b.Entries, clipMemory(cause.Error(), 1000), time.Now())
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

// ErrInvalidChange is a user's edit or move that cannot be made.
var ErrInvalidChange = errors.New("invalid memory change")

// List lists the memories of one scope; workspaceID names the workspace of
// the workspace scope, and only it.
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

// Move puts a memory in another scope, when the model chose the wrong one.
// The workspace must exist, and the topic be free there.
func (m *Manager) Move(id string, scope repo.MemoryScope, workspaceID string) (repo.MemoryRow, error) {
	if err := checkPlace(scope, workspaceID); err != nil {
		return repo.MemoryRow{}, err
	}
	var out repo.MemoryRow
	err := m.db.Tx(func(tx *repo.DB) error {
		e, err := tx.Memory(id)
		if err != nil {
			return err
		}
		if e.Status == repo.MemoryForgotten {
			return fmt.Errorf("%w: a forgotten memory cannot be moved", ErrInvalidChange)
		}
		if e.Scope == scope && e.WorkspaceID == workspaceID {
			out = e
			return nil
		}
		if workspaceID != "" {
			if _, err := tx.Workspace(workspaceID); err != nil {
				return fmt.Errorf("workspace %s: %w", workspaceID, err)
			}
		}
		there, err := tx.Memories(scope, workspaceID, false)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(there, func(o repo.MemoryRow) bool { return o.Topic == e.Topic }) {
			return fmt.Errorf("%w: a memory with the topic %q is already there; forget or edit that one instead", ErrInvalidChange, e.Topic)
		}
		e.Scope, e.WorkspaceID, e.UpdatedAt = scope, workspaceID, time.Now().UTC()
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

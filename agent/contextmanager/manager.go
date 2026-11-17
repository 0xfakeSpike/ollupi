package contextmanager

import (
	"encoding/json"
	"fmt"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

type Manager struct {
	model       string
	inputTokens int
}

func New(model string, limits llm.Limits) *Manager {
	return &Manager{model: model, inputTokens: limits.Input()}
}

const (
	autoCompactPercent = 80
	compactMinPercent  = 10
	responsePercent    = 25
	minPooledTokens    = 512
	artifactPercent    = 15
	toolPercent        = 10
	messagePercent     = 25
	sentLayout         = "Mon 2006-01-02 15:04 -07:00"
)

func share(inputTokens, pct int) int { return max(256, inputTokens*pct/100) }

type Input struct {
	Items  []db.ItemRow
	System string
	Tools  any
}

type Prompt struct {
	System          string
	Messages        []llm.ChatMessage
	NeedsCompaction bool
	Stats           db.ContextStats
}

func (m *Manager) Build(in Input) (Prompt, error) {
	compactable, natural := 0, 0
	obs := newObservations(in.Items, m.artifactBudget())
	p, err := m.fitted(func(limit int) (Prompt, error) {
		messages, fixed, err := m.transcript(obs, in.Items, limit, false)
		if err != nil {
			return Prompt{}, err
		}
		p := Prompt{System: in.System, Messages: messages}
		p.Stats = m.stats(in.System, messages, in.Tools)
		if s := snapshot(in.Items); s != nil {
			p.Stats.MemoryTokens = llm.EstimateTokens(s.Text)
			p.Stats.GlobalMemory, p.Stats.LocalMemory = memorySection(s.Memory, true), memorySection(s.Memory, false)
		}
		m.calibrate(&p.Stats, in.Items)
		if limit == maxObservationTokens {
			compactable, natural = scaled(p.Stats, p.Stats.MessagesTokens-fixed), p.Stats.CalibratedTokens
		}
		return p, nil
	})
	if err != nil {
		return Prompt{}, err
	}
	p.NeedsCompaction = natural > p.Stats.CompactAtTokens &&
		hasItemsAfterCompaction(in.Items) && compactable >= share(m.inputTokens, compactMinPercent)
	if p.Stats.CalibratedTokens > m.inputTokens && !p.NeedsCompaction {
		return Prompt{}, fmt.Errorf("context: the prompt needs %d tokens even with the latest tool results clipped and nothing is left to compact; model %s allows %d", p.Stats.CalibratedTokens, m.model, m.inputTokens)
	}
	return p, nil
}

func (m *Manager) fitted(build func(limit int) (Prompt, error)) (Prompt, error) {
	for limit := maxObservationTokens; ; limit = max(minOutputTokens, limit/2) {
		p, err := build(limit)
		if err != nil || p.Stats.CalibratedTokens <= m.inputTokens || limit == minOutputTokens {
			return p, err
		}
	}
}

func scaled(s db.ContextStats, tokens int) int {
	if s.EstimatedTokens > 0 && s.CalibratedTokens > s.EstimatedTokens {
		return tokens * s.CalibratedTokens / s.EstimatedTokens
	}
	return tokens
}

func (m *Manager) stats(system string, messages []llm.ChatMessage, tools any) db.ContextStats {
	s := db.ContextStats{
		Model:           m.model,
		BudgetTokens:    m.inputTokens,
		CompactAtTokens: m.inputTokens * autoCompactPercent / 100,
		SystemTokens:    llm.EstimateTokens(system),
		MessagesTokens:  llm.EstimateTokens(prettyJSON(messages)) + partTokens(messages),
		ToolsTokens:     llm.EstimateTokens(prettyJSON(tools)),
	}
	s.EstimatedTokens = s.SystemTokens + s.MessagesTokens + s.ToolsTokens
	s.CalibratedTokens = s.EstimatedTokens
	return s
}

func (m *Manager) calibrate(s *db.ContextStats, items []db.ItemRow) {
	from := latestCompaction(items)
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		ref, usage := it.ContextStats, it.Usage
		if it.ResponseID == "" || ref == nil || usage == nil || usage.InputTokens <= 0 || ref.EstimatedTokens <= 0 || ref.Model != m.model {
			continue
		}
		if i > from && it.Type != db.ItemContextCompaction {
			s.CalibratedTokens = max(0, int(usage.InputTokens)+s.EstimatedTokens-ref.EstimatedTokens)
		} else {
			s.CalibratedTokens = max(s.EstimatedTokens, int(int64(s.EstimatedTokens)*usage.InputTokens/int64(ref.EstimatedTokens)))
		}
		return
	}
}

func (m *Manager) InputTokens() int { return m.inputTokens }

func (m *Manager) SkillBudget() int {
	return share(m.inputTokens, 4)
}

func (m *Manager) MessageBudget() int {
	return share(m.inputTokens, messagePercent)
}

func (m *Manager) MemoryBudget() int {
	return max(0, min(4000, m.inputTokens*8/100))
}

func (m *Manager) artifactBudget() int {
	return share(m.inputTokens, artifactPercent)
}

func snapshot(items []db.ItemRow) *db.ItemRow {
	for i := len(items) - 1; i >= 0; i-- {
		if it := &items[i]; it.Type == db.ItemMemoryContext && it.Status == db.ItemCompleted {
			return it
		}
	}
	return nil
}

func memorySection(entries []db.MemoryReference, global bool) *db.MemoryStats {
	s := &db.MemoryStats{IDs: []string{}}
	var shown []db.MemoryNote
	for _, e := range entries {
		if (e.Scope == db.MemoryGlobal) == global {
			s.IDs = append(s.IDs, e.ID)
			shown = append(shown, e.MemoryNote)
		}
	}
	if len(shown) > 0 {
		s.Tokens = llm.EstimateTokens(prettyJSON(shown))
	}
	return s
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("contextmanager: render %T: %v", v, err))
	}
	return string(b)
}

// The JSON estimate leaves out the images and files of messages.
func partTokens(messages []llm.ChatMessage) int {
	n := 0
	for _, m := range messages {
		for _, p := range m.Parts {
			n += p.Tokens
		}
	}
	return n
}

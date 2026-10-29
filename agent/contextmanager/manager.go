package contextmanager

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
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
	fixedPercent       = 50
	artifactPercent    = 15
	toolPercent        = 10
	sentLayout         = "Mon 2006-01-02 15:04 -07:00"
)

func share(inputTokens, pct int) int { return max(256, inputTokens*pct/100) }

func (m *Manager) Fits(system string, tools any) error {
	s := m.stats(system, nil, tools)
	if limit := m.inputTokens * fixedPercent / 100; s.EstimatedTokens > limit {
		return fmt.Errorf("context: the system prompt and tool definitions take %d tokens, over half of the %d input tokens model %s allows", s.EstimatedTokens, m.inputTokens, m.model)
	}
	return nil
}

type Input struct {
	Items  []repo.ItemRow
	System string
	Tools  any
}

type Prompt struct {
	System          string
	Messages        []llm.ChatMessage
	NeedsCompaction bool
	Stats           repo.ContextStats
}

func (m *Manager) Build(in Input) (Prompt, error) {
	compactable, natural := 0, 0
	obs := newObservations()
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

func scaled(s repo.ContextStats, tokens int) int {
	if s.EstimatedTokens > 0 && s.CalibratedTokens > s.EstimatedTokens {
		return tokens * s.CalibratedTokens / s.EstimatedTokens
	}
	return tokens
}

func (m *Manager) stats(system string, messages []llm.ChatMessage, tools any) repo.ContextStats {
	s := repo.ContextStats{
		Model:           m.model,
		BudgetTokens:    m.inputTokens,
		CompactAtTokens: m.inputTokens * autoCompactPercent / 100,
		SystemTokens:    llm.EstimateTokens(system),
		MessagesTokens:  llm.EstimateTokens(prettyJSON(messages)),
		ToolsTokens:     llm.EstimateTokens(prettyJSON(tools)),
	}
	s.EstimatedTokens = s.SystemTokens + s.MessagesTokens + s.ToolsTokens
	s.CalibratedTokens = s.EstimatedTokens
	return s
}

func (m *Manager) calibrate(s *repo.ContextStats, items []repo.ItemRow) {
	from := latestCompaction(items)
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		ref, usage := it.ContextStats, it.Usage
		if it.ResponseID == "" || ref == nil || usage == nil || usage.InputTokens <= 0 || ref.EstimatedTokens <= 0 || ref.Model != m.model {
			continue
		}
		if i > from && it.Type != repo.ItemContextCompaction {
			s.CalibratedTokens = max(0, int(usage.InputTokens)+s.EstimatedTokens-ref.EstimatedTokens)
		} else {
			s.CalibratedTokens = max(s.EstimatedTokens, int(int64(s.EstimatedTokens)*usage.InputTokens/int64(ref.EstimatedTokens)))
		}
		return
	}
}

func (m *Manager) SkillBudget() int {
	return share(m.inputTokens, 4)
}

func (m *Manager) MemoryBudget() int {
	return max(0, min(4000, m.inputTokens*8/100))
}

func (m *Manager) ToolsFit(tools any) bool {
	return llm.EstimateTokens(prettyJSON(tools)) <= share(m.inputTokens, toolPercent)
}

func (m *Manager) ArtifactBudget() int {
	return share(m.inputTokens, artifactPercent)
}

func (m *Manager) CheckArtifacts(artifacts []tools.ContextArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	if need, budget := llm.EstimateTokens(renderArtifacts(artifacts)), m.ArtifactBudget(); need > budget {
		return fmt.Errorf("the tool's context artifacts need about %d tokens; model %s leaves %d for them", need, m.model, budget)
	}
	return nil
}

func snapshot(items []repo.ItemRow) *repo.ItemRow {
	for i := len(items) - 1; i >= 0; i-- {
		if it := &items[i]; it.Type == repo.ItemMemoryContext && it.Status == repo.ItemCompleted {
			return it
		}
	}
	return nil
}

func memorySection(entries []repo.MemoryReference, global bool) *repo.MemoryStats {
	s := &repo.MemoryStats{IDs: []string{}}
	var shown []repo.MemoryNote
	for _, e := range entries {
		if (e.Scope == repo.MemoryGlobal) == global {
			s.IDs = append(s.IDs, e.ID)
			shown = append(shown, e.MemoryNote)
		}
	}
	if len(shown) > 0 {
		s.Tokens = llm.EstimateTokens(prettyJSON(shown))
	}
	return s
}

func (m *Manager) observationBudget(calls, limit int) int {
	return min(limit, max(minPooledTokens, min(m.inputTokens/8, share(m.inputTokens, responsePercent)/max(1, calls))))
}

func (m *Manager) transcript(obs *observations, items []repo.ItemRow, limit int, compacting bool) ([]llm.ChatMessage, int, error) {
	var out []llm.ChatMessage
	latest := ""
	if !compacting {
		latest = latestResponse(items)
	}
	if s := snapshot(items); s != nil && s.Text != "" && !compacting {
		out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: s.Text})
	}
	start := 0
	if c := latestCompaction(items); c >= 0 {
		out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: m.compactedHistory(items[:c], items[c].Text)})
		start = c + 1
	}
	fixed := slices.Clone(out)
	for i := start; i < len(items); {
		it := items[i]
		switch {
		case it.Type == repo.ItemError && it.ContextStats != nil:

			i++
		case it.Type == repo.ItemUserMessage || it.Type == repo.ItemError:
			content := it.Text
			if it.Type == repo.ItemUserMessage && !it.StartedAt.IsZero() {
				content = "[sent " + it.StartedAt.In(time.Local).Format(sentLayout) + "]\n" + content
			}
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: content})
			i++
		case it.Type == repo.ItemMemoryContext || it.Type == repo.ItemContextCompaction || it.Type == repo.ItemToolProgress:

			i++
		case it.ResponseID != "":
			assistant := llm.ChatMessage{Role: llm.RoleAssistant}
			var calls []llm.ChatMessage
			n := 0
			for k := i; k < len(items) && items[k].ResponseID == it.ResponseID; k++ {
				if items[k].IsCall() {
					n++
				}
			}
			budget := maxObservationTokens
			if compacting || it.ResponseID == latest {
				budget = limit
			}
			perObservation := m.observationBudget(n, budget)
			j := i
			for ; j < len(items) && items[j].ResponseID == it.ResponseID; j++ {
				part := items[j]
				switch {
				case part.Type == repo.ItemReasoning || part.Type == repo.ItemAgentMessage:

					if part.Status != repo.ItemCompleted {
						continue
					}
					if part.Type == repo.ItemReasoning {
						assistant.ReasoningContent += part.Text
					} else {
						assistant.Content += part.Text
					}
				case part.IsCall():
					assistant.ToolCalls = append(assistant.ToolCalls, llm.ToolCall{ID: part.CallID, Name: part.Tool, Arguments: part.Arguments})
					content, err := obs.callResult(&items[j], perObservation)
					if err != nil {
						return nil, 0, err
					}
					calls = append(calls, llm.ChatMessage{Role: llm.RoleTool, ToolCallID: part.CallID, Content: content})
				default:
					return nil, 0, fmt.Errorf("context: item %s (%s) cannot be part of a model response", part.ID, part.Type)
				}
			}

			if assistant.Content != "" || len(assistant.ToolCalls) > 0 {
				out = append(out, assistant)
				out = append(out, calls...)
			}
			i = j
		default:
			return nil, 0, fmt.Errorf("context: item %s (%s) has no place in the transcript", it.ID, it.Type)
		}
	}
	fixedTokens := 0
	if len(fixed) > 0 {
		fixedTokens = llm.EstimateTokens(prettyJSON(fixed))
	}
	return out, fixedTokens, nil
}

func latestResponse(items []repo.ItemRow) string {
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.ResponseID != "" && (it.IsCall() || it.Type == repo.ItemReasoning || it.Type == repo.ItemAgentMessage) {
			return it.ResponseID
		}
	}
	return ""
}

func activeArtifacts(items []repo.ItemRow) []tools.ContextArtifact {
	var out []tools.ContextArtifact
	for _, it := range items {
		if !it.IsCall() || it.Status != repo.ItemCompleted || it.Result == nil {
			continue
		}
		for _, artifact := range it.Result.Artifacts {
			out = slices.DeleteFunc(out, func(a tools.ContextArtifact) bool { return a.Kind == artifact.Kind && a.Name == artifact.Name })
			out = append(out, artifact)
		}
	}
	return out
}

func renderArtifact(a tools.ContextArtifact) string {
	return fmt.Sprintf("--- %s: %s ---\n%s", a.Kind, a.Name, a.Content)
}

func renderArtifacts(artifacts []tools.ContextArtifact) string {
	parts := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		parts = append(parts, renderArtifact(a))
	}
	return strings.Join(parts, "\n\n")
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("contextmanager: render %T: %v", v, err))
	}
	return string(b)
}

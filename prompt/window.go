package prompt

import (
	"encoding/json"
	"fmt"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

type Window struct {
	model       string
	inputTokens int
}

func NewWindow(model string, limits llm.Limits) Window {
	return Window{model: model, inputTokens: limits.Input()}
}

const (
	autoCompactPercent = 80
	compactMinPercent  = 10
	responsePercent    = 25
	minPooledTokens    = 512
	artifactPercent    = 15
	toolPercent        = 10
	sentLayout         = "Mon 2006-01-02 15:04 -07:00"
)

func share(inputTokens, pct int) int { return max(minOutputTokens, inputTokens*pct/100) }

func (w Window) InputTokens() int { return w.inputTokens }

func (w Window) SkillBudget() int { return share(w.inputTokens, 4) }

func (w Window) MemoryBudget() int { return max(0, min(4000, w.inputTokens*8/100)) }

func (w Window) artifactBudget() int { return share(w.inputTokens, artifactPercent) }

type Input struct {
	Items     []conversation.Item
	Responses []conversation.Response
	System    string
	Tools     []llm.ToolSpec
}

func Transcript(v conversation.View) []conversation.Item {
	active, _ := v.Active()
	var items []conversation.Item
	for _, t := range v.Turns {
		if t.Ended != nil || t.ID == active.ID {
			items = append(items, v.ItemsOf(t.ID)...)
		}
	}
	return items
}

type Request struct {
	System          string
	Messages        []llm.ChatMessage
	NeedsCompaction bool
	Stats           conversation.ContextStats
}

func (w Window) Build(in Input) (Request, error) {
	compactable, natural := 0, 0
	obs := newObservations(in.Items, w.artifactBudget())
	natives := nativesOf(in.Responses)
	r, err := w.fitted(func(limit int) (Request, error) {
		messages, fixed, err := w.transcript(obs, natives, in.Items, limit, false)
		if err != nil {
			return Request{}, err
		}
		r := Request{System: in.System, Messages: messages}
		r.Stats = w.stats(in.System, messages, in.Tools)
		if s := snapshot(in.Items); s != nil {
			r.Stats.MemoryTokens = llm.EstimateTokens(s.Text)
			r.Stats.GlobalMemory, r.Stats.LocalMemory = memory.Section(s.Memories, true), memory.Section(s.Memories, false)
		}
		w.calibrate(&r.Stats, in.Items, in.Responses)
		if limit == maxObservationTokens {
			compactable, natural = scaled(r.Stats, r.Stats.MessagesTokens-fixed), r.Stats.CalibratedTokens
		}
		return r, nil
	})
	if err != nil {
		return Request{}, err
	}
	r.NeedsCompaction = natural > r.Stats.CompactAtTokens &&
		hasItemsAfterCompaction(in.Items) && compactable >= share(w.inputTokens, compactMinPercent)
	if r.Stats.CalibratedTokens > w.inputTokens && !r.NeedsCompaction {
		return Request{}, fmt.Errorf("prompt: the request needs %d tokens even with the latest tool results clipped and nothing is left to compact; model %s allows %d", r.Stats.CalibratedTokens, w.model, w.inputTokens)
	}
	return r, nil
}

func (w Window) fitted(build func(limit int) (Request, error)) (Request, error) {
	for limit := maxObservationTokens; ; limit = max(minOutputTokens, limit/2) {
		r, err := build(limit)
		if err != nil || r.Stats.CalibratedTokens <= w.inputTokens || limit == minOutputTokens {
			return r, err
		}
	}
}

func scaled(s conversation.ContextStats, tokens int) int {
	if s.EstimatedTokens > 0 && s.CalibratedTokens > s.EstimatedTokens {
		return tokens * s.CalibratedTokens / s.EstimatedTokens
	}
	return tokens
}

func (w Window) stats(system string, messages []llm.ChatMessage, tools []llm.ToolSpec) conversation.ContextStats {
	s := conversation.ContextStats{
		Model:           w.model,
		BudgetTokens:    w.inputTokens,
		CompactAtTokens: w.inputTokens * autoCompactPercent / 100,
		SystemTokens:    llm.EstimateTokens(system),
		MessagesTokens:  llm.EstimateTokens(prettyJSON(messages)) + partTokens(messages),
		ToolsTokens:     llm.EstimateTokens(prettyJSON(tools)),
	}
	s.EstimatedTokens = s.SystemTokens + s.MessagesTokens + s.ToolsTokens
	s.CalibratedTokens = s.EstimatedTokens
	return s
}

func (w Window) calibrate(s *conversation.ContextStats, items []conversation.Item, responses []conversation.Response) {
	compaction := latestCompaction(items)
	from, compactions := int64(0), map[string]bool{}
	if compaction >= 0 {
		from = items[compaction].Seq
	}
	for _, it := range items {
		if it.Kind == conversation.ItemCompaction {
			compactions[it.Response] = true
		}
	}
	for i := len(responses) - 1; i >= 0; i-- {
		r := responses[i]
		if r.Usage.InputTokens <= 0 || r.Stats.EstimatedTokens <= 0 || r.Stats.Model != w.model {
			continue
		}
		if r.Seq > from && !compactions[r.ID] {
			s.CalibratedTokens = max(0, int(r.Usage.InputTokens)+s.EstimatedTokens-r.Stats.EstimatedTokens)
		} else {
			s.CalibratedTokens = max(s.EstimatedTokens, int(int64(s.EstimatedTokens)*r.Usage.InputTokens/int64(r.Stats.EstimatedTokens)))
		}
		return
	}
}

func nativesOf(responses []conversation.Response) map[string][]byte {
	out := map[string][]byte{}
	for _, r := range responses {
		if r.Native != nil {
			out[r.ID] = r.Native
		}
	}
	return out
}

func snapshot(items []conversation.Item) *conversation.Item {
	for i := len(items) - 1; i >= 0; i-- {
		if it := &items[i]; it.Kind == conversation.ItemMemory {
			return it
		}
	}
	return nil
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("prompt: render %T: %v", v, err))
	}
	return string(b)
}

func partTokens(messages []llm.ChatMessage) int {
	n := 0
	for _, m := range messages {
		for _, p := range m.Parts {
			n += p.Tokens
		}
	}
	return n
}

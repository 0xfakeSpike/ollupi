package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/pkg/render"
)

// observations renders each call's result once per budget. The latest skills loaded, as many as fit the artifact
// budget together, are shown, so that loading skills, or one skill again and again, cannot fill the window.
type observations struct {
	texts    map[string]string
	spans    map[string][][2]int
	rendered map[observationKey]string
	shown    map[shownArtifact]bool
	last     map[[2]string]string
}

type shownArtifact struct {
	item, kind, name string
}

type observationKey struct {
	id     string
	budget int
}

func newObservations(items []conversation.Item, artifactBudget int) *observations {
	o := &observations{texts: map[string]string{}, spans: map[string][][2]int{}, rendered: map[observationKey]string{},
		shown: map[shownArtifact]bool{}, last: map[[2]string]string{}}
	for _, it := range items {
		if completedCall(it) {
			for _, a := range it.Result.Artifacts {
				o.last[[2]string{a.Kind, a.Name}] = it.ID
			}
		}
	}
	kept, _ := recentArtifacts(activeArtifacts(items), artifactBudget)
	for _, a := range kept {
		o.shown[shownArtifact{o.last[[2]string{a.Kind, a.Name}], a.Kind, a.Name}] = true
	}
	return o
}

func completedCall(it conversation.Item) bool {
	return it.Kind == conversation.ItemToolCall && it.Status == conversation.Done && it.Result != nil
}

func (o *observations) artifacts(itemID string, artifacts []conversation.Artifact) string {
	parts := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		switch {
		case o.shown[shownArtifact{itemID, a.Kind, a.Name}]:
			parts = append(parts, renderArtifact(a))
		case o.last[[2]string{a.Kind, a.Name}] != itemID:
			parts = append(parts, fmt.Sprintf("--- %s %s: loaded again later, and shown there ---", a.Kind, a.Name))
		default:
			parts = append(parts, fmt.Sprintf("--- %s %s: no longer fits with what was loaded since; load it again if it is still needed ---", a.Kind, a.Name))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (o *observations) callResult(it conversation.Item, outputBudget int) string {
	switch {
	case it.Result != nil:
		key := observationKey{it.ID, outputBudget}
		if text, ok := o.rendered[key]; ok {
			return text
		}
		text := o.render(it, outputBudget)
		o.rendered[key] = text
		return text
	case it.Status == conversation.Errored || it.Status == conversation.Declined:
		return "error: " + clipText(it.Error, outputBudget)
	}
	return "error: the call did not finish"
}

const (
	maxObservationTokens = 6000
	minOutputTokens      = 256
)

func (o *observations) render(it conversation.Item, budget int) string {
	text, ok := o.texts[it.ID]
	if !ok {
		text = OutputText(it.Result.Output)
		o.texts[it.ID] = text
	}
	spans := func(string) [][2]int {
		spans, ok := o.spans[it.ID]
		if !ok {
			spans = redact.Spans(text)
			o.spans[it.ID] = spans
		}
		return spans
	}
	artifacts := o.artifacts(it.ID, it.Result.Artifacts)
	if text == "" {
		text = "(no output)"
	} else {
		text = clip(text, max(minOutputTokens, budget-llm.EstimateTokens(artifacts)), spans, readable(it.ID))
	}
	if artifacts != "" {
		text += "\n\n" + artifacts
	}
	return text
}

// OutputText is a tool's output as the model reads it, and as read_tool_output counts its characters.
func OutputText(raw json.RawMessage) string { return render.Output(decoded(raw), nil) }

// The output as it was given, numbers as written.
func decoded(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return string(raw)
	}
	return v
}

func clipText(s string, budget int) string {
	return clip(s, budget, redact.Spans, omitted)
}

func omitted(from, to int) string {
	return fmt.Sprintf("\n[... %d characters omitted ...]\n", to-from)
}

func readable(id string) func(from, to int) string {
	return func(from, to int) string {
		return fmt.Sprintf("\n[... characters %d-%d of this output omitted; call %s with output_id %q and offset %d to read them ...]\n", from, to, agent.ReadOutputTool, id, from)
	}
}

func clip(s string, budget int, spans func(string) [][2]int, marker func(from, to int) string) string {
	s = strings.TrimSpace(s)
	if s == "" || budget <= 0 || llm.EstimateTokens(s) <= budget {
		return s
	}
	runes := []rune(s)
	lo, hi := 0, len(runes)/2
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if llm.EstimateTokens(string(runes[:mid])+marker(mid, len(runes)-mid)+string(runes[len(runes)-mid:])) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	head, tail := redact.Cuts(s, spans(s), 2*lo/3, len(runes)-(2*lo-2*lo/3))
	return strings.TrimSpace(string(runes[:head])) + marker(head, tail) + strings.TrimSpace(string(runes[tail:]))
}

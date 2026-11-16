package contextmanager

import (
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/pkg/render"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/outputs"
)

type observations struct {
	results  map[string]tools.Result
	texts    map[string]string
	spans    map[string][][2]int
	rendered map[observationKey]string
	// shown are the artifacts the transcript carries, each in the result that loaded it last: as many of the latest
	// as fit the artifact budget together, so that loading skills, or one skill again and again, cannot fill the window.
	shown map[shownArtifact]bool
	// last is the result that loaded each artifact last, by kind and name.
	last map[[2]string]string
}

type shownArtifact struct {
	item, kind, name string
}

type observationKey struct {
	id     string
	budget int
}

func newObservations(items []db.ItemRow, artifactBudget int) *observations {
	o := &observations{results: map[string]tools.Result{}, texts: map[string]string{}, spans: map[string][][2]int{}, rendered: map[observationKey]string{},
		shown: map[shownArtifact]bool{}, last: map[[2]string]string{}}
	for _, it := range items {
		if it.IsCall() && it.Status == db.ItemCompleted {
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

// artifacts renders the artifacts of a result: those the transcript carries in full, and the others by name.
func (o *observations) artifacts(itemID string, artifacts []tools.ContextArtifact) string {
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

func (o *observations) callResult(it *db.ItemRow, outputBudget int) (string, error) {
	switch {
	case it.Result != nil:
		key := observationKey{it.ID, outputBudget}
		if text, ok := o.rendered[key]; ok {
			return text, nil
		}
		text, err := o.render(it, outputBudget)
		if err == nil {
			o.rendered[key] = text
		}
		return text, err
	case it.Status == db.ItemFailed || it.Status == db.ItemDeclined:
		return "error: " + clipText(it.Error, outputBudget), nil
	default:
		return "error: the call did not finish", nil
	}
}

const (
	maxObservationTokens = 6000
	minOutputTokens      = 256
)

func (o *observations) render(it *db.ItemRow, budget int) (string, error) {
	result, ok := o.results[it.ID]
	if !ok {
		var err error
		if result, err = it.LiveResult(); err != nil {
			return "", fmt.Errorf("context: %w", err)
		}
		o.results[it.ID] = result
	}
	text, ok := o.texts[it.ID]
	if !ok {
		text = render.Output(result.Output, result.Err)
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
	if result.Err != nil {
		return "error: " + clip(text, budget, spans, readable(it.ID)), nil
	}
	artifacts := o.artifacts(it.ID, result.Artifacts)
	if text == "" {
		text = "(no output)"
	} else {
		text = clip(text, max(minOutputTokens, budget-llm.EstimateTokens(artifacts)), spans, readable(it.ID))
	}
	if artifacts != "" {
		text += "\n\n" + artifacts
	}
	return text, nil
}

func clipText(s string, budget int) string {
	return clip(s, budget, redact.Spans, omitted)
}

func omitted(from, to int) string {
	return fmt.Sprintf("\n[... %d characters omitted ...]\n", to-from)
}

func readable(id string) func(from, to int) string {
	return func(from, to int) string {
		return fmt.Sprintf("\n[... characters %d-%d of this output omitted; call %s with output_id %q and offset %d to read them ...]\n", from, to, outputs.Tool, id, from)
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

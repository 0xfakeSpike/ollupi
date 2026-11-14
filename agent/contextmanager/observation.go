package contextmanager

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func observationText(r tools.Result) string {
	if r.Err != nil {
		return strings.TrimSpace(r.Err.Error())
	}
	v := compactStructured(r.Output)
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("contextmanager: render output of %s: %v", r.Tool, err))
	}
	return strings.TrimSpace(string(b))
}

func compactStructured(v any) any {
	switch x := v.(type) {
	case string:
		return compactDecorativeLines(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = compactStructured(value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = compactStructured(x[i])
		}
		return out
	default:
		return v
	}
}

func compactDecorativeLines(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	skipping, first := false, true
	for line := range strings.SplitSeq(s, "\n") {
		if isDecorativeLine(line) {
			if !skipping {
				if !first {
					b.WriteByte('\n')
				}
				b.WriteString("...")
				first = false
				skipping = true
			}
			continue
		}
		skipping = false
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		first = false
	}
	return b.String()
}

func isDecorativeLine(line string) bool {
	t := strings.TrimSpace(line)
	var runes, nonSpace, border int
	for _, r := range t {
		runes++
		if unicode.IsSpace(r) {
			continue
		}
		nonSpace++
		if (r >= 0x2500 && r <= 0x257f) || strings.ContainsRune("-=*_+|", r) {
			border++
		}
	}
	return runes >= 3 && nonSpace >= 3 && border*100 >= 90*nonSpace
}

func cutPoints(s string, spans [][2]int, head, tail int) (int, int) {
	for _, sp := range spans {
		start, end := utf8.RuneCountInString(s[:sp[0]]), utf8.RuneCountInString(s[:sp[1]])
		if start < head && head < end {
			head = start
		}
		if start < tail && tail < end {
			tail = end
		}
	}
	return head, tail
}

type observations struct {
	results  map[string]tools.Result
	texts    map[string]string
	spans    map[string][][2]int
	rendered map[observationKey]string
}

type observationKey struct {
	id     string
	budget int
}

func newObservations() *observations {
	return &observations{results: map[string]tools.Result{}, texts: map[string]string{}, spans: map[string][][2]int{}, rendered: map[observationKey]string{}}
}

func (o *observations) callResult(it *repo.ItemRow, outputBudget int) (string, error) {
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
	case it.Status == repo.ItemFailed || it.Status == repo.ItemDeclined:
		return "error: " + clipText(it.Error, outputBudget), nil
	default:
		return "error: the call did not finish", nil
	}
}

const (
	maxObservationTokens = 6000
	minOutputTokens      = 256
)

func (o *observations) render(it *repo.ItemRow, budget int) (string, error) {
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
		text = observationText(result)
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
	artifacts := renderArtifacts(result.Artifacts)
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
		return fmt.Sprintf("\n[... characters %d-%d of this output omitted; call %s with output_id %q and offset %d to read them ...]\n", from, to, ToolReadOutput, id, from)
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

	head, tail := cutPoints(s, spans(s), 2*lo/3, len(runes)-(2*lo-2*lo/3))
	return strings.TrimSpace(string(runes[:head])) + marker(head, tail) + strings.TrimSpace(string(runes[tail:]))
}

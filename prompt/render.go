package prompt

import (
	"encoding/json"
	"strings"
	"unicode"
)

func OutputText(raw json.RawMessage) string {
	v := compactStructured(decoded(raw))
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(prettyJSON(v))
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
	var kept []string
	for line := range strings.SplitSeq(s, "\n") {
		if !isDecorativeLine(line) {
			kept = append(kept, line)
		} else if len(kept) == 0 || kept[len(kept)-1] != "..." {
			kept = append(kept, "...")
		}
	}
	return strings.Join(kept, "\n")
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

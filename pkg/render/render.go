// Package render reads a call's output as the text it shows: structured output as indented JSON, and in each string of
// it a run of decorative lines, such as a table's borders, as one "...". A model reads outputs so, and where one is
// shortened for it, the offsets it is told count the characters of this text: whatever reads the rest renders it so.
package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Output is the text of a call's output, or of err, which the call failed with when it is set.
func Output(output any, err error) string {
	if err != nil {
		return strings.TrimSpace(err.Error())
	}
	v := compactStructured(output)
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	b, merr := json.MarshalIndent(v, "", "  ")
	if merr != nil {
		panic(fmt.Sprintf("render: an output that is no JSON value: %v", merr))
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

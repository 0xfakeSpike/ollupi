package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

type crashing struct{}

func (crashing) Name() (string, string) { return "crash", "Crashes or answers too much." }

func (crashing) ToolList(context.Context) ([]*Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "additionalProperties": false}
	return []*Spec{{Name: "crash", InputSchema: schema}, {Name: "flood", InputSchema: schema}}, nil
}

func (crashing) Invoke(_ context.Context, _ Caller, tool string, _ map[string]any) (Result, error) {
	if tool == "crash" {
		var m map[string]int
		m["boom"]++
	}
	// Three-byte characters after two bytes put the limit inside a character.
	return Result{Output: "ab" + strings.Repeat("中", maxOutputBytes)}, nil
}

// A tool that panics, answers too much, or is called wrongly comes back as a result, and the process goes on.
func TestTheRegistryTurnsEveryFailureIntoAResult(t *testing.T) {
	r, err := New(context.Background(), nil, crashing{})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.Invoke(context.Background(), Caller{}, "crash", map[string]any{}); res.Err == nil || !strings.Contains(res.Err.Error(), "crashed") {
		t.Errorf("a panic: %+v", res)
	}
	if res := r.Invoke(context.Background(), Caller{}, "flood", map[string]any{}); res.Err != nil || !strings.Contains(res.Output.(string), "output truncated") || !utf8.ValidString(res.Output.(string)) {
		t.Errorf("an answer over the limit: %v, valid UTF-8 %v", res.Err, utf8.ValidString(res.Output.(string)))
	}
	if res := r.Invoke(context.Background(), Caller{}, "flood", map[string]any{"n": "one"}); res.Err == nil {
		t.Error("arguments that do not fit the schema were run")
	}
	if res := r.Invoke(context.Background(), Caller{}, "missing", map[string]any{}); res.Err == nil {
		t.Error("an unknown tool was run")
	}
}

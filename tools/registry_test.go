package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

type crashing struct{}

func (crashing) Name() (string, string) { return "crash", "Crashes or answers too much." }

func (crashing) ToolList(context.Context) ([]*Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "additionalProperties": false}
	return []*Spec{{Name: "crash", InputSchema: schema}, {Name: "flood", InputSchema: schema}, {Name: "fail", InputSchema: schema}}, nil
}

func (crashing) Invoke(_ context.Context, _ Caller, tool string, _ map[string]any) (Result, error) {
	if tool == "crash" {
		var m map[string]int
		m["boom"]++
	}
	// Three-byte characters after two bytes put the limit inside a character.
	flood := "ab" + strings.Repeat("中", maxOutputBytes)
	if tool == "fail" {
		return Result{Output: flood, Err: errors.New(flood)}, nil
	}
	return Result{Output: flood}, nil
}

// A tool that panics, answers too much, or is called wrongly comes back as a result, and the process goes on.
func TestTheRegistryTurnsEveryFailureIntoAResult(t *testing.T) {
	r, err := New(context.Background(), nil, crashing{})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.InvokePrepared(context.Background(), Caller{}, "crash", map[string]any{}, nil); res.Err == nil || !strings.Contains(res.Err.Error(), "crashed") {
		t.Errorf("a panic: %+v", res)
	}
	if res := r.InvokePrepared(context.Background(), Caller{}, "flood", map[string]any{}, nil); res.Err != nil || !strings.Contains(res.Output.(string), "output truncated") || !utf8.ValidString(res.Output.(string)) {
		t.Errorf("an answer over the limit: %v, valid UTF-8 %v", res.Err, utf8.ValidString(res.Output.(string)))
	}
	if res := r.InvokePrepared(context.Background(), Caller{}, "fail", map[string]any{}, nil); res.Err == nil || !strings.Contains(res.Err.Error(), "error truncated") ||
		len(res.Err.Error()) > maxErrorBytes+100 || !utf8.ValidString(res.Err.Error()) || !strings.Contains(res.Output.(string), "output truncated") {
		t.Errorf("a failure over the limit: %d bytes of error", len(res.Err.Error()))
	}
	if res := r.InvokePrepared(context.Background(), Caller{}, "flood", map[string]any{"n": "one"}, nil); res.Err == nil {
		t.Error("arguments that do not fit the schema were run")
	}
	if res := r.InvokePrepared(context.Background(), Caller{}, "missing", map[string]any{}, nil); res.Err == nil {
		t.Error("an unknown tool was run")
	}
}

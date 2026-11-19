package kit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/tool"
)

type crashing struct{}

func (crashing) Name() (string, string) { return "crash", "Crashes or answers too much." }

func (crashing) Tools(context.Context) ([]Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "additionalProperties": false}
	return []Spec{{Name: "crash", InputSchema: schema}, {Name: "flood", InputSchema: schema}, {Name: "fail", InputSchema: schema}}, nil
}

func (crashing) Invoke(_ context.Context, c tool.Call) (Result, error) {
	if c.Tool == "crash" {
		var m map[string]int
		m["boom"]++
	}
	flood := "ab" + strings.Repeat("中", maxOutputBytes)
	if c.Tool == "fail" {
		return Result{Output: flood}, errors.New(flood)
	}
	return Result{Output: flood}, nil
}

func call(name string, args map[string]any) tool.Call { return tool.Call{Tool: name, Arguments: args} }

func text(raw json.RawMessage) string {
	var s string
	json.Unmarshal(raw, &s)
	return s
}

func TestTheRegistryTurnsEveryFailureIntoAResult(t *testing.T) {
	r, err := New(context.Background(), nil, crashing{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if res := r.Invoke(ctx, call("crash", map[string]any{})); !strings.Contains(res.Error, "crashed") {
		t.Errorf("a panic: %+v", res)
	}
	if res := r.Invoke(ctx, call("flood", map[string]any{})); res.Error != "" || !strings.Contains(text(res.Output), "output truncated") || !utf8.ValidString(text(res.Output)) {
		t.Errorf("an answer over the limit: %q", res.Error)
	}
	if res := r.Invoke(ctx, call("fail", map[string]any{})); !strings.Contains(res.Error, "error truncated") || len(res.Error) > maxErrorBytes+100 || !utf8.ValidString(res.Error) {
		t.Errorf("a failure over the limit: %d bytes of error", len(res.Error))
	}
	if res := r.Invoke(ctx, call("flood", map[string]any{"n": "one"})); res.Error == "" {
		t.Error("arguments that do not fit the schema were run")
	}
	if res := r.Invoke(ctx, call("missing", map[string]any{})); res.Error == "" {
		t.Error("an unknown tool was run")
	}
	specs := r.Specs()
	if len(specs) != 5 || specs[0].Provider != "crash" || specs[len(specs)-1].Provider != introspectionProvider {
		t.Errorf("specs %+v", specs)
	}
}

type slow struct{}

func (slow) Name() (string, string) { return "slow", "Runs long." }

func (slow) Tools(context.Context) ([]Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "additionalProperties": false}
	return []Spec{{Name: "build", InputSchema: schema, Background: tool.BackgroundOptional}, {Name: "count", InputSchema: schema}}, nil
}

func (slow) Invoke(_ context.Context, c tool.Call) (Result, error) { return Result{Output: c.Tool}, nil }

func TestAToolThatMayRunInTheBackgroundTakesTheArgument(t *testing.T) {
	r, err := New(context.Background(), nil, slow{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if res := r.Invoke(ctx, call("build", map[string]any{"n": 1, tool.BackgroundArgument: true})); res.Error != "" {
		t.Errorf("a background build: %s", res.Error)
	}
	if res := r.Invoke(ctx, call("count", map[string]any{tool.BackgroundArgument: true})); res.Error == "" {
		t.Error("a tool that runs only in the foreground took the background argument")
	}
	for _, s := range r.Specs() {
		properties, _ := s.Schema["properties"].(map[string]any)
		_, listed := properties[tool.BackgroundArgument]
		if want := s.Name == "build"; listed != want || s.InBackground(map[string]any{tool.BackgroundArgument: true}) != want {
			t.Errorf("%s lists the argument %v", s.Name, listed)
		}
	}
}

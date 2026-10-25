package contextmanager

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

const (
	ToolReadOutput = "read_tool_output"

	defaultReadRunes = 8000
	maxReadRunes     = 20000
)

type Outputs struct {
	db *repo.DB
}

func NewOutputs(db *repo.DB) *Outputs { return &Outputs{db: db} }

func (*Outputs) Name() (string, string) {
	return "context", "Reads tool output that was shortened in this conversation."
}

func (*Outputs) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: ToolReadOutput,
		Description: "Reads characters of a tool output that the conversation shows shortened. " +
			"A shortened output names its output_id and the offset of the omitted characters; read only the part you need.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"output_id": map[string]any{"type": "string", "minLength": 1},
				"offset":    map[string]any{"type": "integer", "minimum": 0, "description": "First character to read, counted from 0."},
				"length":    map[string]any{"type": "integer", "minimum": 1, "maximum": maxReadRunes, "description": fmt.Sprintf("Characters to read; default %d.", defaultReadRunes)},
			},
			"required":             []string{"output_id", "offset"},
			"additionalProperties": false,
		},
		Annotations: &tools.Annotations{Title: "Read tool output", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (o *Outputs) Invoke(ctx context.Context, _ string, args map[string]any) (tools.Result, error) {
	id, _ := args["output_id"].(string)
	offset, err := intArg(args, "offset", 0)
	if err != nil {
		return tools.Result{}, err
	}
	length, err := intArg(args, "length", defaultReadRunes)
	if err != nil {
		return tools.Result{}, err
	}
	if offset < 0 || length < 1 || length > maxReadRunes {
		return tools.Result{}, fmt.Errorf("offset must be at least 0 and length between 1 and %d", maxReadRunes)
	}
	it, err := o.db.Item(id)
	if repo.IsNotFound(err) || err == nil && (it.ThreadID != tools.CallerOf(ctx).ThreadID || !it.IsCall() || it.Result == nil) {
		return tools.Result{}, fmt.Errorf("this conversation has no tool output %q", id)
	}
	if err != nil {
		return tools.Result{}, err
	}
	result, err := it.LiveResult()
	if err != nil {
		return tools.Result{}, err
	}
	text := []rune(observationText(result))
	if offset >= len(text) {
		return tools.Result{}, fmt.Errorf("offset %d is past the end of the output, which has %d characters", offset, len(text))
	}
	end := min(len(text), offset+length)
	return tools.Result{Output: fmt.Sprintf("Characters %d-%d of %d:\n%s", offset, end, len(text), string(text[offset:end]))}, nil
}

func intArg(args map[string]any, name string, fallback int) (int, error) {
	v, ok := args[name]
	if !ok {
		return fallback, nil
	}
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return int(f), nil
}

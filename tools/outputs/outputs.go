// Package outputs gives the model read_tool_output, which reads the part of a tool output that the conversation shows
// shortened: agent/contextmanager cuts a long output, and names the characters it left out, of the output's text as
// pkg/render renders it.
package outputs

import (
	"context"
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/pkg/render"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

const (
	// Tool reads a shortened tool output.
	Tool = "read_tool_output"
	// ProviderName is the provider of Tool.
	ProviderName = "context"

	defaultReadRunes = 8000
	maxReadRunes     = 20000
)

// Provider gives Tool, reading the outputs from the conversations' items.
type Provider struct {
	db *db.DB
}

func New(store *db.DB) *Provider { return &Provider{db: store} }

func (*Provider) Name() (string, string) {
	return ProviderName, "Reads tool output that was shortened in this conversation."
}

func (*Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: Tool,
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

func (p *Provider) Invoke(ctx context.Context, caller tools.Caller, _ string, args map[string]any) (tools.Result, error) {
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
	it, err := p.db.Item(id)
	if errors.Is(err, db.ErrNotFound) || err == nil && (it.ThreadID != caller.ThreadID || !it.IsCall() && it.Type != db.ItemAgentReport || it.Result == nil) {
		return tools.Result{}, fmt.Errorf("this conversation has no tool output %q", id)
	}
	if err != nil {
		return tools.Result{}, err
	}
	result, err := it.LiveResult()
	if err != nil {
		return tools.Result{}, err
	}
	text := []rune(render.Output(result.Output, result.Err))
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

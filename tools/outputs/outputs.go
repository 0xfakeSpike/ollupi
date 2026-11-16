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
	// The schema holds offset and length to whole numbers in their ranges.
	offset, _ := args["offset"].(float64)
	length := float64(defaultReadRunes)
	if l, ok := args["length"].(float64); ok {
		length = l
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
	start := int(offset)
	if start >= len(text) {
		return tools.Result{}, fmt.Errorf("offset %d is past the end of the output, which has %d characters", start, len(text))
	}
	end := min(len(text), start+int(length))
	return tools.Result{Output: fmt.Sprintf("Characters %d-%d of %d:\n%s", start, end, len(text), string(text[start:end]))}, nil
}

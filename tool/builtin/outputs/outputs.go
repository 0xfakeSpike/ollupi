// Package outputs gives the model read_tool_output, which reads the part of a tool output the conversation shows
// shortened: the prompt cuts a long output and names the characters it left out, of the output's text as it renders it.
package outputs

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

const (
	defaultReadRunes = 8000
	maxReadRunes     = 20000
)

type Provider struct{ st store.Store }

func New(st store.Store) Provider { return Provider{st: st} }

func (Provider) Name() (string, string) {
	return agent.OutputsProvider, "Reads tool output that was shortened in this conversation."
}

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: agent.ReadOutputTool,
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
		Annotations: tool.Annotations{Title: "Read tool output", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: kit.Hint(false)},
	}}, nil
}

func (p Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	id, _ := c.Arguments["output_id"].(string)
	// The schema holds offset and length to whole numbers in their ranges.
	offset, _ := c.Arguments["offset"].(float64)
	length := float64(defaultReadRunes)
	if l, ok := c.Arguments["length"].(float64); ok {
		length = l
	}
	var v conversation.View
	if err := p.st.Read(ctx, func(r store.Reader) (err error) { v, err = r.View(c.Thread); return err }); err != nil {
		return kit.Result{}, err
	}
	it, ok := v.ItemOf(id)
	if !ok || it.Result == nil || (it.Kind != conversation.ItemToolCall && it.Kind != conversation.ItemReport) {
		return kit.Result{}, fmt.Errorf("this conversation has no tool output %q", id)
	}
	text := []rune(prompt.OutputText(it.Result.Output))
	start := int(offset)
	if start >= len(text) {
		return kit.Result{}, fmt.Errorf("offset %d is past the end of the output, which has %d characters", start, len(text))
	}
	end := min(len(text), start+int(length))
	return kit.Result{Output: fmt.Sprintf("Characters %d-%d of %d:\n%s", start, end, len(text), string(text[start:end]))}, nil
}

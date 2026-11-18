package outputs

import (
	"cmp"
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
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

type readArgs struct {
	OutputID string `json:"output_id"`
	Offset   int    `json:"offset" jsonschema:"First character to read, counted from 0."`
	Length   int    `json:"length,omitempty"`
}

func (readArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["output_id"].MinLength = jsonschema.Ptr(1)
	s.Properties["offset"].Minimum = jsonschema.Ptr(0.0)
	s.Properties["length"].Minimum, s.Properties["length"].Maximum = jsonschema.Ptr(1.0), jsonschema.Ptr(float64(maxReadRunes))
	s.Properties["length"].Description = fmt.Sprintf("Characters to read; default %d.", defaultReadRunes)
}

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: agent.ReadOutputTool,
		Description: "Reads characters of a tool output that the conversation shows shortened. " +
			"A shortened output names its output_id and the offset of the omitted characters; read only the part you need.",
		InputSchema: kit.InputSchema[readArgs](),
		Annotations: tool.Annotations{Title: "Read tool output", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
	}}, nil
}

func (p Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, err := kit.DecodeArguments[readArgs](c.Arguments)
	if err != nil {
		return kit.Result{}, err
	}
	var v conversation.View
	if err := p.st.Read(ctx, func(r store.Reader) (err error) { v, err = r.View(c.Thread); return err }); err != nil {
		return kit.Result{}, err
	}
	it, ok := v.ItemOf(args.OutputID)
	if !ok || it.Result == nil || (it.Kind != conversation.ItemToolCall && it.Kind != conversation.ItemReport) {
		return kit.Result{}, fmt.Errorf("this conversation has no tool output %q", args.OutputID)
	}
	text := []rune(prompt.OutputText(it.Result.Output))
	start := args.Offset
	if start >= len(text) {
		return kit.Result{}, fmt.Errorf("offset %d is past the end of the output, which has %d characters", start, len(text))
	}
	end := min(len(text), start+cmp.Or(args.Length, defaultReadRunes))
	return kit.Result{Output: fmt.Sprintf("Characters %d-%d of %d:\n%s", start, end, len(text), string(text[start:end]))}, nil
}

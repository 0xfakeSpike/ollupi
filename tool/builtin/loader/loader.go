package loader

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

const ProviderName = "deferred_tools"

type Provider struct{}

type loadArgs struct {
	Names []string `json:"names" jsonschema:"Exact tool names from DEFERRED TOOLS."`
}

func (loadArgs) Constrain(s *jsonschema.Schema) {
	names := s.Properties["names"]
	names.MinItems = jsonschema.Ptr(1)
	names.Items.MinLength = jsonschema.Ptr(1)
}

func (Provider) Name() (string, string) {
	return ProviderName, "Loads the tools named in DEFERRED TOOLS."
}

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name:        agent.LoadToolsTool,
		Annotations: tool.Annotations{ReadOnlyHint: true, OpenWorldHint: kit.Hint(false)},
		Description: "Load deferred tools so you can call them. A loaded tool stays callable for the rest of the conversation.",
		InputSchema: kit.InputSchema[loadArgs](),
	}}, nil
}

func (Provider) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	args, _ := kit.DecodeArguments[loadArgs](c.Arguments)
	loaded := make([]string, 0, len(args.Names))
	for _, name := range args.Names {
		if !slices.Contains(c.Deferred, name) {
			return kit.Result{}, fmt.Errorf("%s: %q is not a deferred tool; take names from DEFERRED TOOLS, and call a tool that is already in the tool list directly", agent.LoadToolsTool, name)
		}
		if !slices.Contains(loaded, name) {
			loaded = append(loaded, name)
		}
	}
	return kit.Result{Output: map[string]any{"loaded": loaded}}, nil
}

package runtime

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// Loader gives the model load_tools. A call only checks the names against the thread's deferred tools:
// Offer reads the loaded ones back from the thread's completed load_tools calls.
type Loader struct{}

type loadArgs struct {
	Names []string `json:"names" jsonschema:"Exact tool names from DEFERRED TOOLS."`
}

func (loadArgs) Constrain(s *jsonschema.Schema) {
	names := s.Properties["names"]
	names.MinItems = jsonschema.Ptr(1)
	names.Items.MinLength = jsonschema.Ptr(1)
}

func (Loader) Name() (string, string) {
	return "deferred_tools", "Loads the tools named in DEFERRED TOOLS."
}

func (Loader) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name:        tools.ToolLoadTools,
		Annotations: &tools.Annotations{ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		Description: "Load deferred tools so you can call them. A loaded tool stays callable for the rest of the conversation.",
		InputSchema: tools.InputSchema[loadArgs](),
	}}, nil
}

func (Loader) Invoke(ctx context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[loadArgs](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	deferred, _ := ctx.Value(deferredKey{}).([]tools.ToolDescriptor)
	loaded := make([]string, 0, len(args.Names))
	for _, name := range args.Names {
		if !slices.ContainsFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name }) {
			return tools.Result{}, fmt.Errorf("load_tools: %q is not a deferred tool; take names from DEFERRED TOOLS, and call a tool that is already in the tool list directly", name)
		}
		if !slices.Contains(loaded, name) {
			loaded = append(loaded, name)
		}
	}
	return tools.Result{Output: map[string]any{"loaded": loaded}}, nil
}

// deferredKey carries the thread's deferred tools to Loader.Invoke.
type deferredKey struct{}

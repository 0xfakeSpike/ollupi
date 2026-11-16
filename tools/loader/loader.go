// Package loader gives the model load_tools, which loads the tools a run defers: the ones it names to the model in
// DEFERRED TOOLS without sending their definitions, as agent/contextmanager splits them when they do not all fit.
package loader

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	// Tool loads deferred tools.
	Tool = "load_tools"
	// ProviderName is the provider of Tool.
	ProviderName = "deferred_tools"
)

// Provider gives Tool. A call only checks the names against the tools its run defers, as the caller names them: the
// role's Offer reads the loaded ones back from the conversation's completed calls.
type Provider struct{}

func New() *Provider { return &Provider{} }

type loadArgs struct {
	Names []string `json:"names" jsonschema:"Exact tool names from DEFERRED TOOLS."`
}

func (loadArgs) Constrain(s *jsonschema.Schema) {
	names := s.Properties["names"]
	names.MinItems = jsonschema.Ptr(1)
	names.Items.MinLength = jsonschema.Ptr(1)
}

func (*Provider) Name() (string, string) {
	return ProviderName, "Loads the tools named in DEFERRED TOOLS."
}

func (*Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name:        Tool,
		Annotations: &tools.Annotations{ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		Description: "Load deferred tools so you can call them. A loaded tool stays callable for the rest of the conversation.",
		InputSchema: tools.InputSchema[loadArgs](),
	}}, nil
}

func (*Provider) Invoke(_ context.Context, caller tools.Caller, _ string, arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[loadArgs](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	loaded := make([]string, 0, len(args.Names))
	for _, name := range args.Names {
		if !slices.Contains(caller.Deferred, name) {
			return tools.Result{}, fmt.Errorf("%s: %q is not a deferred tool; take names from DEFERRED TOOLS, and call a tool that is already in the tool list directly", Tool, name)
		}
		if !slices.Contains(loaded, name) {
			loaded = append(loaded, name)
		}
	}
	return tools.Result{Output: map[string]any{"loaded": loaded}}, nil
}

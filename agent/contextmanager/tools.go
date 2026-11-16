package contextmanager

import (
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Specs are the tools as the model is sent them.
func Specs(descriptors []tools.ToolDescriptor) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(descriptors))
	for _, d := range descriptors {
		specs = append(specs, llm.ToolSpec{Name: d.Name, Description: strings.TrimSpace(d.Description), Parameters: d.InputSchema})
	}
	return specs
}

// Split splits the visible tools into the ones sent to the model and the ones only named, for load_tools to load, so
// that the ones sent fit the tool budget. All are sent when they fit, or when load_tools is not among them to load the
// others. Otherwise the external providers' tools wait first, then the built-in tools other than core, which an empty
// core leaves sent; load_tools is sent when some tools wait, and only then.
func (m *Manager) Split(visible []tools.ToolDescriptor, core []string) (sent, deferred []tools.ToolDescriptor) {
	loader := func(d tools.ToolDescriptor) bool { return d.Name == loader.Tool }
	external := func(d tools.ToolDescriptor) bool { return strings.Contains(d.Name, tools.ProviderSeparator) }
	all := slices.DeleteFunc(slices.Clone(visible), loader)
	if !slices.ContainsFunc(visible, loader) || m.toolsFit(all) {
		return all, nil
	}
	keeps := []func(tools.ToolDescriptor) bool{
		func(d tools.ToolDescriptor) bool { return !external(d) },
		func(d tools.ToolDescriptor) bool { return !external(d) && slices.Contains(core, d.Name) },
	}
	if len(core) == 0 {
		keeps = keeps[:1]
	}
	for _, keep := range keeps {
		sent, deferred = nil, nil
		for _, d := range visible {
			switch {
			case loader(d) || keep(d):
				sent = append(sent, d)
			default:
				deferred = append(deferred, d)
			}
		}
		if m.toolsFit(sent) {
			break
		}
	}
	if len(deferred) == 0 {
		return all, nil
	}
	return sent, deferred
}

func (m *Manager) toolsFit(descriptors []tools.ToolDescriptor) bool {
	return llm.EstimateTokens(prettyJSON(Specs(descriptors))) <= share(m.inputTokens, toolPercent)
}

package role

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Offer splits the visible tools into the ones sent to the model and the ones only named in DEFERRED TOOLS. Only the
// core tools are sent when the role has them; otherwise tools of external providers are deferred when all visible
// tools would take more than the tool budget. The ones a load_tools call loaded follow the others in load order, so the
// tool list only grows.
func (r Role) Offer(visible []tools.ToolDescriptor, window *contextmanager.Manager, items []repo.ItemRow) (offered, deferred []tools.ToolDescriptor) {
	visible = launchable(visible)
	external := func(d tools.ToolDescriptor) bool { return strings.Contains(d.Name, tools.ProviderSeparator) }
	loader := func(d tools.ToolDescriptor) bool { return d.Name == tools.ToolLoadTools }
	deferrable := func(d tools.ToolDescriptor) bool {
		return external(d) || r.Core != nil && !slices.Contains(r.Core, d.Name)
	}
	deferring := slices.ContainsFunc(visible, loader) && (r.Core != nil || slices.ContainsFunc(visible, external) && !window.ToolsFit(Specs(visible)))
	for _, d := range visible {
		switch {
		case deferring && deferrable(d):
			deferred = append(deferred, d)
		case deferring || !loader(d):
			offered = append(offered, d)
		}
	}
	for _, name := range loadedTools(items) {
		i := slices.IndexFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i >= 0 && !slices.ContainsFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name }) {
			offered = append(offered, deferred[i])
		}
	}
	return offered, deferred
}

// launchable leaves out the launcher of a subagent role that works with one provider while none of that provider's
// tools is visible: its subagent would have nothing to work with.
func launchable(visible []tools.ToolDescriptor) []tools.ToolDescriptor {
	return slices.DeleteFunc(slices.Clone(visible), func(d tools.ToolDescriptor) bool {
		r, ok := Launched(d.Name)
		return ok && r.Provider != "" && !slices.ContainsFunc(visible, func(t tools.ToolDescriptor) bool { return t.Provider == r.Provider })
	})
}

func loadedTools(items []repo.ItemRow) []string {
	var names []string
	for _, it := range items {
		if it.Tool != tools.ToolLoadTools || it.Status != repo.ItemCompleted || it.Result == nil {
			continue
		}
		var out struct {
			Loaded []string `json:"loaded"`
		}
		if json.Unmarshal(it.Result.Output, &out) == nil {
			names = append(names, out.Loaded...)
		}
	}
	return names
}

// Specs are the tools as the model is sent them.
func Specs(descriptors []tools.ToolDescriptor) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(descriptors))
	for _, d := range descriptors {
		specs = append(specs, llm.ToolSpec{Name: d.Name, Description: strings.TrimSpace(d.Description), Parameters: d.ModelSchema()})
	}
	return specs
}

package role

import (
	"encoding/json"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"github.com/0xfakeSpike/ollupi/tools/outputs"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// offer is what a role offers of the visible tools: the ones the window fits around core, the built-in tools the role
// is sent whatever it holds (none: every built-in tool), and the ones a load_tools call loaded, which follow the
// others in load order, so the tool list only grows.
func offer(visible []tools.ToolDescriptor, window *contextmanager.Manager, items []db.ItemRow, core []string) (offered, deferred []tools.ToolDescriptor) {
	offered, deferred = window.Split(launchable(visible), core)
	for _, name := range loadedTools(items) {
		i := slices.IndexFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i >= 0 && !slices.ContainsFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name }) {
			offered = append(offered, deferred[i])
		}
	}
	return offered, deferred
}

// launchable leaves out the tool of each kind of subagent whose role's layer keeps none of the visible tools: its
// subagent would have nothing to work with.
func launchable(visible []tools.ToolDescriptor) []tools.ToolDescriptor {
	layers := map[string]permission.Layer{string(GeneralSub): generalSubLayer, string(BrowserSub): browserSubLayer}
	out := slices.Clone(visible)
	for name := range subagent.Kinds {
		layer, ok := layers[name]
		if ok && slices.ContainsFunc(visible, func(t tools.ToolDescriptor) bool {
			return t.Provider != outputs.ProviderName && layer.Keeps(t)
		}) {
			continue
		}
		out = slices.DeleteFunc(out, func(d tools.ToolDescriptor) bool { return d.Name == name })
	}
	return out
}

func loadedTools(items []db.ItemRow) []string {
	var names []string
	for _, it := range items {
		if it.Tool != loader.Tool || it.Status != db.ItemCompleted || it.Result == nil {
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

package role

import (
	"encoding/json"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"github.com/0xfakeSpike/ollupi/tools/outputs"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// Loaded tools follow the others in load order, so the tool list only grows.
func (rt *Runtime) Offer() (offered, deferred []tools.ToolDescriptor) {
	offered, deferred = rt.window.Split(launchable(rt.checker.Tools()), rt.role.core)
	for _, name := range rt.loadedTools() {
		i := slices.IndexFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name })
		alreadyOffered := slices.ContainsFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i >= 0 && !alreadyOffered {
			offered = append(offered, deferred[i])
		}
	}
	return offered, deferred
}

// A subagent whose role's layer keeps none of the visible tools would have nothing to work with.
func launchable(visible []tools.ToolDescriptor) []tools.ToolDescriptor {
	layers := map[string]permission.Layer{string(GeneralSub): generalSubLayer, string(BrowserSub): browserSubLayer}
	out := slices.Clone(visible)
	for name := range subagent.Kinds {
		layer := layers[name]
		workable := slices.ContainsFunc(visible, func(t tools.ToolDescriptor) bool {
			return t.Provider != outputs.ProviderName && layer.Keeps(t)
		})
		if workable {
			continue
		}
		out = slices.DeleteFunc(out, func(d tools.ToolDescriptor) bool { return d.Name == name })
	}
	return out
}

func (rt *Runtime) loadedTools() []string {
	var names []string
	for _, it := range rt.conversation() {
		if it.Tool != loader.Tool || it.Status != db.ItemCompleted {
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

func (rt *Runtime) tool(name string) (tools.ToolDescriptor, bool) {
	offered, _ := rt.Offer()
	i := slices.IndexFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return offered[i], true
}

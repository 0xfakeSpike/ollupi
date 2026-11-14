package role

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

// Offers says whether the role is offered d. Every role keeps read_tool_output, which a shortened output points at. A
// subagent cannot launch another or schedule tasks, which would come back to its thread after it reported. The rest is
// the role's own choice.
func (r Role) Offers(d tools.ToolDescriptor) bool {
	if d.Name == contextmanager.ToolReadOutput {
		return true
	}
	if r.Launcher != nil {
		if _, launches := Launched(d.Name); launches || d.Provider == cron.ProviderName {
			return false
		}
	}
	return r.offers(d)
}

// Offer splits the visible tools into the ones sent to the model and the ones only named in DEFERRED TOOLS, as the
// role sends them. The ones a load_tools call loaded follow the others in load order, so the tool list only grows.
func (r Role) Offer(visible []tools.ToolDescriptor, window *contextmanager.Manager, items []repo.ItemRow) (offered, deferred []tools.ToolDescriptor) {
	offered, deferred = r.send(launchable(visible), window)
	for _, name := range loadedTools(items) {
		i := slices.IndexFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i >= 0 && !slices.ContainsFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name }) {
			offered = append(offered, deferred[i])
		}
	}
	return offered, deferred
}

// launchable leaves out the launcher of a subagent role that is offered none of the visible tools: its subagent would
// have nothing to work with.
func launchable(visible []tools.ToolDescriptor) []tools.ToolDescriptor {
	return slices.DeleteFunc(slices.Clone(visible), func(d tools.ToolDescriptor) bool {
		r, ok := Launched(d.Name)
		return ok && !slices.ContainsFunc(visible, func(t tools.ToolDescriptor) bool {
			return t.Name != contextmanager.ToolReadOutput && r.Offers(t)
		})
	})
}

func anyTool(tools.ToolDescriptor) bool { return true }

func external(d tools.ToolDescriptor) bool { return strings.Contains(d.Name, tools.ProviderSeparator) }

func loader(d tools.ToolDescriptor) bool { return d.Name == tools.ToolLoadTools }

// sendFitting sends every tool, but defers the tools of external providers when all the tools would take more than the
// tool budget and load_tools can load them. load_tools is sent only when there are deferred tools to load.
func sendFitting(visible []tools.ToolDescriptor, window *contextmanager.Manager) (sent, deferred []tools.ToolDescriptor) {
	deferring := slices.ContainsFunc(visible, loader) && slices.ContainsFunc(visible, external) && !window.ToolsFit(Specs(visible))
	for _, d := range visible {
		switch {
		case deferring && external(d):
			deferred = append(deferred, d)
		case deferring || !loader(d):
			sent = append(sent, d)
		}
	}
	return sent, deferred
}

// sendOnly sends only the core tools, and defers the others for load_tools to load; without load_tools it sends every
// tool.
func sendOnly(core ...string) func([]tools.ToolDescriptor, *contextmanager.Manager) ([]tools.ToolDescriptor, []tools.ToolDescriptor) {
	return func(visible []tools.ToolDescriptor, _ *contextmanager.Manager) (sent, deferred []tools.ToolDescriptor) {
		if !slices.ContainsFunc(visible, loader) {
			return visible, nil
		}
		for _, d := range visible {
			if slices.Contains(core, d.Name) && !external(d) {
				sent = append(sent, d)
			} else {
				deferred = append(deferred, d)
			}
		}
		return sent, deferred
	}
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

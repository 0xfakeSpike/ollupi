package prompt

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
)

type Tool struct {
	Name        string
	Provider    string
	Description string
	Schema      map[string]any
}

func Specs(tools []Tool) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, llm.ToolSpec{Name: t.Name, Description: strings.TrimSpace(t.Description), Parameters: t.Schema})
	}
	return specs
}

func (w Window) Offer(visible []Tool, core []string, transcript []conversation.Item) (sent, deferred []Tool) {
	sent, deferred = w.split(visible, core)
	for _, name := range loaded(transcript) {
		i := slices.IndexFunc(deferred, func(t Tool) bool { return t.Name == name })
		if i >= 0 && !slices.ContainsFunc(sent, func(t Tool) bool { return t.Name == name }) {
			sent = append(sent, deferred[i])
		}
	}
	return sent, deferred
}

func (w Window) split(visible []Tool, core []string) (sent, deferred []Tool) {
	loader := func(t Tool) bool { return t.Name == agent.LoadToolsTool }
	external := func(t Tool) bool { return strings.Contains(t.Name, policy.ProviderSeparator) }
	all := slices.DeleteFunc(slices.Clone(visible), loader)
	if !slices.ContainsFunc(visible, loader) || w.toolsFit(all) {
		return all, nil
	}
	builtin := func(t Tool) bool { return loader(t) || !external(t) }
	sent, deferred = partition(visible, builtin)
	if !w.toolsFit(sent) && len(core) > 0 {
		builtinCore := func(t Tool) bool { return loader(t) || !external(t) && slices.Contains(core, t.Name) }
		sent, deferred = partition(visible, builtinCore)
	}
	if len(deferred) == 0 {
		return all, nil
	}
	return sent, deferred
}

func partition(tools []Tool, keep func(Tool) bool) (kept, rest []Tool) {
	for _, t := range tools {
		if keep(t) {
			kept = append(kept, t)
		} else {
			rest = append(rest, t)
		}
	}
	return kept, rest
}

func (w Window) toolsFit(tools []Tool) bool {
	return llm.EstimateTokens(prettyJSON(Specs(tools))) <= percentOf(w.inputTokens, toolPercent)
}

func loaded(transcript []conversation.Item) []string {
	var names []string
	for _, it := range transcript {
		if !completedCall(it) || it.Call.Tool != agent.LoadToolsTool {
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

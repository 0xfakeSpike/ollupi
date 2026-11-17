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

// Tool is a tool as the model is offered it.
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

// Offer splits the visible tools into those sent with their definitions and those only named, when not all fit: the
// built-in ones are sent before the external, the role's core ones before the rest. A tool the model has loaded is
// sent from then on, after the others, so that the list only grows.
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
	keeps := []func(Tool) bool{
		func(t Tool) bool { return !external(t) },
		func(t Tool) bool { return !external(t) && slices.Contains(core, t.Name) },
	}
	if len(core) == 0 {
		keeps = keeps[:1]
	}
	for _, keep := range keeps {
		sent, deferred = nil, nil
		for _, t := range visible {
			if loader(t) || keep(t) {
				sent = append(sent, t)
			} else {
				deferred = append(deferred, t)
			}
		}
		if w.toolsFit(sent) {
			break
		}
	}
	if len(deferred) == 0 {
		return all, nil
	}
	return sent, deferred
}

func (w Window) toolsFit(tools []Tool) bool {
	return llm.EstimateTokens(prettyJSON(Specs(tools))) <= share(w.inputTokens, toolPercent)
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

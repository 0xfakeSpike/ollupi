// Package role holds the roles of agent, side by side: the two main agents and the subagents, each defined in a file
// of its own; and the Runtime that runs a turn as the role it names. Each run, or its recovery after a crash, rebuilds
// that agent from the role and the turn's items alone: they are the run's state, and each of its steps is read from
// them.
package role

import (
	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/memory"
)

// Name is a role as a turn stores it.
type Name string

var NameValues = []Name{FullMain, CompactMain, GeneralSub, BrowserSub}

// A Role is one role of agent: what a run of a turn that names it is made of, besides the deployment it serves.
type Role struct {
	// name is the role as a turn stores it.
	name Name
	// rules are the role's own rules, which its system prompt is built around.
	rules string
	// answersUser says whether the role answers the user, so that its prompt starts with the agent's own; a subagent
	// reports to the agent that launched it instead.
	answersUser bool
	// own is the role's own permission layer, if it has one: what it keeps of the tools, below the user's settings.
	own []permission.Layer
	// core are the built-in tools the role is sent whatever the window holds; none: every built-in tool.
	core []string
	// recalls says whether the role's runs take in the user's memory.
	recalls bool
	// maxModelCalls bounds a turn's responses that may call tools, the final answer it is then asked for being one
	// more; maxToolCalls bounds its tool calls; and after maxConsecutiveFailures calls in a row that failed, tool use
	// stops until the model answers or the user writes.
	maxModelCalls, maxToolCalls, maxConsecutiveFailures int
}

// roles are the roles by name.
var roles = map[Name]Role{FullMain: fullMain, CompactMain: compactMain, GeneralSub: generalSub, BrowserSub: browserSub}

// New makes the runtime of the role a turn names, as it stores it, for the deployment's agent: called name, which the
// operator's prompt may describe, with each role's skills installed in skillsDir; its default permission settings
// serve users with different tools when shared, and tools, when set, are the only tools a turn may use; memory keeps
// the users' memory, which the roles that take it in recall; window is the context window of the turn's model.
func New(role Name, name, prompt, skillsDir string, settings permission.Settings, shared bool, tools []string, memory *memory.Manager, window *contextmanager.Manager) (*Runtime, bool) {
	r, ok := roles[role]
	if !ok {
		return nil, false
	}
	return &Runtime{role: r, agent: name, prompt: prompt, skillsDir: skillsDir, settings: settings, shared: shared, tools: tools, memory: memory, window: window}, true
}

// ForMain is the main role for a model that takes inputTokens of input.
func ForMain(inputTokens int) Name {
	if inputTokens < compactBelow {
		return CompactMain
	}
	return FullMain
}

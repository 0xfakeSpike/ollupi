// Package role holds the roles of agent, side by side: the two main agents and the subagents. A turn names the role
// that runs it, and each run, or its recovery after a crash, rebuilds that agent from the role and the turn alone.
// Each role is defined in a file of its own.
package role

import (
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

// Name is a role as a turn stores it.
type Name string

const (
	FullMain    Name = "full-main-agent"
	CompactMain Name = "compact-main-agent"
	GeneralSub  Name = "general-sub-agent"
	BrowserSub  Name = "browser-sub-agent"
)

var NameValues = []Name{FullMain, CompactMain, GeneralSub, BrowserSub}

// A Role is one role of agent: its system prompt, the tools it is offered and sent, and how it is launched.
type Role struct {
	Name Name
	// Persona puts the agent's own prompt before Rules, and the conversation's skills, deferred tools and project after
	// them. Without it Rules are the whole system prompt.
	Persona bool
	Rules   string
	// Skills lists the skills in the system prompt.
	Skills bool
	// Core are the only tools sent with every request; the others are deferred, for the model to load. Nil sends every
	// tool, deferring external ones only when they do not fit.
	Core []string
	// Provider, when set, is the only provider whose tools the role is offered.
	Provider string
	// Memory loads the user's memory into the run.
	Memory bool
	// Launcher is the tool that launches the role as a subagent; a main role has none.
	Launcher *Launcher
}

// A Launcher is the tool that launches a subagent role, as the launching agent sees it.
type Launcher struct {
	Tool, Title string
	// Description tells the launching agent when to use the role; Launchers adds how launching works.
	Description string
}

// all are the roles, in the order their launchers are listed.
var all = []Role{fullMain, compactMain, generalSub, browserSub}

func Of(name Name) (Role, bool) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.Name == name })
	if i < 0 {
		return Role{}, false
	}
	return all[i], true
}

// Launched is the role that the tool launches.
func Launched(tool string) (Role, bool) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.Launcher != nil && r.Launcher.Tool == tool })
	if i < 0 {
		return Role{}, false
	}
	return all[i], true
}

// compactBelow is the input the full main agent's prompt and tool list need: about 8k tokens, which the context window
// holds to half of it.
const compactBelow = 16_384

// ForMain is the main role for a model that takes inputTokens of input.
func ForMain(inputTokens int) Role {
	if inputTokens < compactBelow {
		return compactMain
	}
	return fullMain
}

// Offers says whether the role is offered d. Every role keeps read_tool_output, which a shortened output points at. A
// main role is offered every tool. A subagent cannot launch another or schedule tasks, which would come back to its
// thread after it reported, and one with a Provider works with that provider's tools alone.
func (r Role) Offers(d tools.ToolDescriptor) bool {
	if r.Launcher == nil || d.Name == contextmanager.ToolReadOutput {
		return true
	}
	if _, launches := Launched(d.Name); launches || d.Provider == cron.ProviderName {
		return false
	}
	return r.Provider == "" || d.Provider == r.Provider
}

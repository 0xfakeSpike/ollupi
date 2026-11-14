// Package role holds the roles of agent, side by side: the two main agents and the subagents. A turn names the role
// that runs it, and each run, or its recovery after a crash, rebuilds that agent from the role and the turn alone.
// Each role is defined in a file of its own.
package role

import (
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Name is a role as a turn stores it.
type Name string

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
	// Permission is the role's own permission layer: the tools it may use, below the user's settings.
	Permission permission.Layer
	// send splits the visible tools into the ones sent to the model and the ones only named in DEFERRED TOOLS.
	send func(visible []tools.ToolDescriptor, window *contextmanager.Manager) (sent, deferred []tools.ToolDescriptor)
	// Memory loads the user's memory into the run.
	Memory bool
	// LaunchedBy is the tool through which another agent launches the role as a subagent; a main role has none.
	LaunchedBy *LaunchTool
}

// A LaunchTool is the tool that launches a subagent role, as the launching agent sees it.
type LaunchTool struct {
	Name, Title string
	// Description tells the launching agent when to use the role; LaunchTools adds how launching works.
	Description string
}

// all are the roles, in the order their launch tools are listed.
var all = []Role{fullMain, compactMain, generalSub, browserSub}

func Of(name Name) (Role, bool) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.Name == name })
	if i < 0 {
		return Role{}, false
	}
	return all[i], true
}

// LaunchedBy is the role that the tool launches.
func LaunchedBy(tool string) (Role, bool) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.LaunchedBy != nil && r.LaunchedBy.Name == tool })
	if i < 0 {
		return Role{}, false
	}
	return all[i], true
}

// ForMain is the main role for a model that takes inputTokens of input.
func ForMain(inputTokens int) Role {
	if inputTokens < compactBelow {
		return compactMain
	}
	return fullMain
}

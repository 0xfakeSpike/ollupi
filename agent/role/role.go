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
	// Persona puts the agent's own prompt before Rules, and the conversation's deferred tools and project after them.
	// Without it Rules and the role's skills are the whole system prompt.
	Persona bool
	Rules   string
	// Permission is the role's own permission layer: the tools it may use, below the user's settings.
	Permission permission.Layer
	// send splits the visible tools into the ones sent to the model and the ones only named in DEFERRED TOOLS.
	send func(visible []tools.ToolDescriptor, window *contextmanager.Manager) (sent, deferred []tools.ToolDescriptor)
	// Memory loads the user's memory into the run.
	Memory bool
	// deployment is what the deployment configures for the role; see Deployed.
	deployment Deployment
}

// all are the roles.
var all = []Role{fullMain, compactMain, generalSub, browserSub}

func Of(name Name) (Role, bool) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.Name == name })
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

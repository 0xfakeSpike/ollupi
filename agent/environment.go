package agent

import (
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

type environment struct {
	workspace repo.WorkspaceRow
	settings  permission.Settings
}

func (a *Agent) environmentOf(t repo.ThreadRow) environment {
	var env environment
	if t.WorkspaceID != "" {
		if w, err := a.DB.Workspace(t.WorkspaceID); err == nil {
			env.workspace = w
		}
	}
	layers := []permission.Settings{a.Settings, env.workspace.Settings, t.Settings}
	if t.ParentThreadID != "" {
		layers = append(layers, permission.Settings{Rules: permission.Rules{Deny: []string{subagent.Tool}}})
	}
	env.settings = permission.Resolve(layers...)
	return env
}

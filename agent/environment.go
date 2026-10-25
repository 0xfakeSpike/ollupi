package agent

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

type environment struct {
	workspace repo.WorkspaceRow
	settings  permission.Settings
}

func (a *orchestrator) environmentOf(t repo.ThreadRow) (environment, error) {
	var env environment
	if t.WorkspaceID != "" {
		w, err := a.DB.Workspace(string(t.WorkspaceID))
		if err != nil {
			return environment{}, fmt.Errorf("agent: workspace %s: %w", t.WorkspaceID, err)
		}
		env.workspace = w
	}
	own := t.Settings
	if t.ParentThreadID != "" {
		parent, err := a.DB.ThreadRow(string(t.ParentThreadID))
		if err != nil {
			return environment{}, fmt.Errorf("agent: parent thread %s: %w", t.ParentThreadID, err)
		}
		own = parent.Settings
	}
	layers := []permission.Settings{a.Settings, env.workspace.Settings, own}
	if t.ParentThreadID != "" {
		layers = append(layers, permission.Settings{Rules: permission.Rules{Deny: []string{subagent.Tool}}})
	}
	env.settings = permission.Resolve(layers...)
	return env, nil
}

func (env environment) caller(t repo.ThreadRow) tools.Caller {
	return tools.Caller{ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), Root: env.workspace.RootPath, Subagent: t.ParentThreadID != ""}
}

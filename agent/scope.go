package agent

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

func (a *Agent) workspaceOf(id repo.Ref) (repo.WorkspaceRow, error) {
	if id == "" {
		return repo.WorkspaceRow{}, nil
	}
	w, err := a.cfg.DB.Workspace(string(id))
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: workspace %s: %w", id, err)
	}
	return w, nil
}

func (a *Agent) threadSettings(t repo.ThreadRow, w repo.WorkspaceRow) (permission.Settings, error) {
	if t.ParentThreadID == "" {
		return permission.Resolve(a.cfg.Settings, w.Settings, t.Settings), nil
	}
	parent, err := a.cfg.DB.ThreadRow(string(t.ParentThreadID))
	if err != nil {
		return permission.Settings{}, fmt.Errorf("agent: parent thread %s: %w", t.ParentThreadID, err)
	}
	noSubagents := permission.Settings{Rules: permission.Rules{Deny: []string{subagent.Tool}}}
	return permission.Resolve(a.cfg.Settings, w.Settings, parent.Settings, noSubagents), nil
}

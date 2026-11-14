package agent

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/repo"
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

// workspaceFor reads a workspace the user names; one of another user's does not exist for them.
func (a *Agent) workspaceFor(user string, id repo.Ref) (repo.WorkspaceRow, error) {
	if id == "" {
		return repo.WorkspaceRow{}, nil
	}
	w, err := a.cfg.DB.WorkspaceOf(user, string(id))
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: workspace %s: %w", id, err)
	}
	return w, nil
}

func (a *Agent) permissions(t repo.ThreadRow, r role.Role) (*permission.Checker, repo.WorkspaceRow, error) {
	w, err := a.workspaceOf(t.WorkspaceID)
	if err != nil {
		return nil, repo.WorkspaceRow{}, err
	}
	layers, err := a.threadLayers(t, w)
	if err != nil {
		return nil, repo.WorkspaceRow{}, err
	}
	registry, err := a.registryFor(t.UserID)
	if err != nil {
		return nil, repo.WorkspaceRow{}, err
	}
	allowlist := []string{}
	for _, d := range registry.Descriptors() {
		if r.Offers(d) && (a.cfg.Tools == nil || slices.Contains(a.cfg.Tools, d.Name)) {
			allowlist = append(allowlist, d.Name)
		}
	}
	checker, err := permission.New(layers, allowlist, registry)
	if err != nil {
		return nil, repo.WorkspaceRow{}, err
	}
	return checker, w, nil
}

func (a *Agent) layers(w repo.WorkspaceRow) []permission.Layer {
	out := []permission.Layer{{Of: "the default permission settings", Settings: a.cfg.Settings, Shared: a.cfg.ToolsFor != nil}}
	if w.ID != "" {
		out = append(out, workspaceLayer(w.Name, w.Settings))
	}
	return out
}

// workspaceLayer and threadLayer name the layers both where settings are validated and where they are enforced.
func workspaceLayer(name string, s permission.Settings) permission.Layer {
	return permission.Layer{Of: fmt.Sprintf("the permission settings of workspace %q", name), Settings: s}
}

func threadLayer(s permission.Settings) permission.Layer {
	return permission.Layer{Of: "this conversation's permission settings", Settings: s}
}

func (a *Agent) threadLayers(t repo.ThreadRow, w repo.WorkspaceRow) ([]permission.Layer, error) {
	if t.ParentThreadID == "" {
		return append(a.layers(w), threadLayer(t.Settings)), nil
	}
	parent, err := a.cfg.DB.ThreadRow(string(t.ParentThreadID))
	if err != nil {
		return nil, fmt.Errorf("agent: parent thread %s: %w", t.ParentThreadID, err)
	}
	return append(a.layers(w), permission.Layer{Of: "the parent conversation's permission settings", Settings: parent.Settings}), nil
}

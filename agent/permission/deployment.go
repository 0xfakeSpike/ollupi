package permission

import (
	"slices"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Deployment struct {
	Settings Settings
	Tools    []string
}

// Users have tools of their own, so the deployment's settings may name tools a user lacks.
func (d Deployment) layer() Layer {
	return Layer{Of: "the default permission settings", Settings: d.Settings, Shared: true}
}

func (d Deployment) Base(u db.SettingsRow, w db.WorkspaceRow) []Layer {
	out := []Layer{d.layer(), UserLayer(u)}
	if w.ID != "" {
		out = append(out, WorkspaceLayer(w.Name, w.Settings))
	}
	return out
}

// own, the role's own layers, sit below the deployment's, the user's, the workspace's and the conversation's, which
// decide over them.
func (d Deployment) Checker(own []Layer, u db.SettingsRow, w db.WorkspaceRow, registry *tools.Registry, conversation ...Layer) (*Checker, error) {
	var allowlist []string
	if d.Tools != nil {
		allowlist = []string{}
		for _, t := range registry.Descriptors() {
			if slices.Contains(d.Tools, t.Name) {
				allowlist = append(allowlist, t.Name)
			}
		}
	}
	return New(slices.Concat(own, d.Base(u, w), conversation), allowlist, registry)
}

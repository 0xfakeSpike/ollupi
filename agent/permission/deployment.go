package permission

import (
	"slices"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Deployment struct {
	Settings Settings
	Shared   bool
	Tools    []string
}

func (d Deployment) Base(w db.WorkspaceRow) []Layer {
	out := []Layer{{Of: "the default permission settings", Settings: d.Settings, Shared: d.Shared}}
	if w.ID != "" {
		out = append(out, WorkspaceLayer(w.Name, w.Settings))
	}
	return out
}

// own, the role's own layers, sit below the deployment's, the workspace's and the conversation's, which decide over them.
func (d Deployment) Checker(own []Layer, w db.WorkspaceRow, registry *tools.Registry, conversation ...Layer) (*Checker, error) {
	var allowlist []string
	if d.Tools != nil {
		allowlist = []string{}
		for _, t := range registry.Descriptors() {
			if slices.Contains(d.Tools, t.Name) {
				allowlist = append(allowlist, t.Name)
			}
		}
	}
	return New(slices.Concat(own, d.Base(w), conversation), allowlist, registry)
}

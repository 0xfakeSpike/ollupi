package permission

import (
	"slices"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// A Deployment is what the deployment permits: its default settings, which serve users with different tools when
// Shared, and, when Tools is set, the only tools a turn may use. Every conversation's permissions are made on it.
type Deployment struct {
	Settings Settings
	Shared   bool
	Tools    []string
}

// Base are the layers every conversation's settings go on: the deployment's default settings, and the settings of the
// conversation's workspace, when it has one.
func (d Deployment) Base(w db.WorkspaceRow) []Layer {
	out := []Layer{{Of: "the default permission settings", Settings: d.Settings, Shared: d.Shared}}
	if w.ID != "" {
		out = append(out, WorkspaceLayer(w.Name, w.Settings))
	}
	return out
}

// Checker makes a run's permissions: own, what its role itself keeps of the tools, below the deployment's default
// settings, the workspace's and the conversation's (none outside one), which decide over it, with the tools of
// registry the deployment allows.
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

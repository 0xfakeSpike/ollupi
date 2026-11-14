package permission

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo"
)

// Base are the layers every conversation's settings go on: the default settings, which serve users with different
// tools when they are shared, and the settings of the conversation's workspace, when it has one.
func Base(defaults Settings, shared bool, w repo.WorkspaceRow) []Layer {
	out := []Layer{{Of: "the default permission settings", Settings: defaults, Shared: shared}}
	if w.ID != "" {
		out = append(out, WorkspaceLayer(w.Name, w.Settings))
	}
	return out
}

// WorkspaceLayer, ThreadLayer and ParentLayer name the layers both where settings are validated and where they are
// enforced.
func WorkspaceLayer(name string, s Settings) Layer {
	return Layer{Of: fmt.Sprintf("the permission settings of workspace %q", name), Settings: s}
}

func ThreadLayer(s Settings) Layer {
	return Layer{Of: "this conversation's permission settings", Settings: s}
}

// ParentLayer is the settings a subagent's conversation goes by: those of the conversation that launched it.
func ParentLayer(s Settings) Layer {
	return Layer{Of: "the parent conversation's permission settings", Settings: s}
}

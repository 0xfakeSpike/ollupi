package permission

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// WorkspaceLayer, ThreadLayer and ParentLayer name the layers both where settings are validated and where they are
// enforced.
func WorkspaceLayer(name string, s Settings) Layer {
	return Layer{Of: fmt.Sprintf("the permission settings of workspace %q", name), Settings: s}
}

// A user who does not use long-term memory is not offered its tools.
func UserLayer(u db.SettingsRow) Layer {
	l := Layer{Of: "your permission settings", Settings: u.Permissions}
	if !u.Memory {
		l.Without = []string{memory.ProviderName}
	}
	return l
}

func ThreadLayer(s Settings) Layer {
	return Layer{Of: "this conversation's permission settings", Settings: s}
}

func ParentLayer(s Settings) Layer {
	return Layer{Of: "the parent conversation's permission settings", Settings: s}
}

type Layer struct {
	Of       string
	Settings Settings
	// Shared settings serve users with different tools, so they may name tools this user lacks.
	Shared    bool
	Providers []string
	Without   []string
}

func (l Layer) Keeps(d tools.ToolDescriptor) bool {
	return (l.Providers == nil || slices.Contains(l.Providers, d.Provider)) && !slices.Contains(l.Without, d.Provider)
}

func Resolve(layers ...Settings) Settings {
	var out Settings
	for _, l := range layers {
		if l.ApprovalPolicy != "" {
			out.ApprovalPolicy = l.ApprovalPolicy
		}
		out.Rules.Allow = appendNew(out.Rules.Allow, l.Rules.Allow)
		out.Rules.Ask = appendNew(out.Rules.Ask, l.Rules.Ask)
		out.Rules.Deny = appendNew(out.Rules.Deny, l.Rules.Deny)
	}
	return out
}

func appendNew(dst, src []string) []string {
	for _, s := range src {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}

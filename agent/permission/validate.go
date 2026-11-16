package permission

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
)

func checkLayer(l Layer, previous *Settings, registry *tools.Registry) error {
	s := l.Settings
	if s.ApprovalPolicy != "" {
		if err := Policy(s.ApprovalPolicy).Validate(); err != nil {
			return err
		}
	}
	set, err := parseRules(s.Rules)
	if err != nil {
		return err
	}
	var kept []string
	if previous != nil {
		for _, text := range slices.Concat(previous.Rules.Allow, previous.Rules.Ask, previous.Rules.Deny) {
			kept = append(kept, strings.TrimSpace(text))
		}
	}
	for _, r := range set.all() {
		switch {
		case registry.Unavailable(r.tool):
			if previous != nil && !slices.Contains(kept, r.text) {
				provider, _, _ := strings.Cut(r.tool, tools.ProviderSeparator)
				return fmt.Errorf("permission rule %q: the MCP server %q is not connected, so its tools cannot be checked now; add this rule once it is connected", r.text, provider)
			}
		case l.Shared && someonesTool(r.tool, registry):
		case !registry.Offers(r.tool):
			return unknownTool(fmt.Sprintf("permission rule %q", r.text), r.tool, registry)
		case r.pattern != nil && !registry.HasPermissionContent(r.tool):
			return fmt.Errorf("permission rule %q: %s takes no specifier; write %q to match every call", r.text, r.tool, r.tool)
		}
	}
	return nil
}

func ValidateLayer(base []Layer, previous Settings, layer Layer, registry *tools.Registry) error {
	if err := checkLayer(layer, &previous, registry); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidSettings, layer.Of, err)
	}
	if _, err := New(append(slices.Clone(base), layer), nil, registry); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSettings, err)
	}
	return nil
}

var ErrInvalidSettings = errors.New("invalid permission settings")

func unknownTool(what, tool string, registry *tools.Registry) error {
	if provider, _, ok := strings.Cut(tool, tools.ProviderSeparator); ok && !registry.HasProvider(provider) {
		return fmt.Errorf("%s: tool %q is not in the tool registry, and no tool provider is named %q; if that MCP server was removed or renamed, remove or rename the rule", what, tool, provider)
	}
	return fmt.Errorf("%s: tool %q is not in the tool registry", what, tool)
}

// someonesTool says a tool belongs to a provider this user lacks, which another user may have.
func someonesTool(tool string, registry *tools.Registry) bool {
	provider, _, ok := strings.Cut(tool, tools.ProviderSeparator)
	return ok && !registry.HasProvider(provider)
}

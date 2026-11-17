package policy

import (
	"fmt"
	"slices"
	"strings"
)

func (s Settings) Check() error {
	if s.ApprovalPolicy != "" {
		if err := Policy(s.ApprovalPolicy).Validate(); err != nil {
			return err
		}
	}
	_, err := parseRules(s.Rules)
	return err
}

func checkLayer(l Layer, previous *Settings, box Toolbox) error {
	if err := l.Settings.Check(); err != nil {
		return err
	}
	set, _ := parseRules(l.Settings.Rules)
	var kept []string
	if previous != nil {
		for _, text := range slices.Concat(previous.Rules.Allow, previous.Rules.Ask, previous.Rules.Deny) {
			kept = append(kept, strings.TrimSpace(text))
		}
	}
	for _, r := range set.all() {
		switch {
		case box.unavailable(r.tool):
			if previous != nil && !slices.Contains(kept, r.text) {
				provider, _, _ := strings.Cut(r.tool, ProviderSeparator)
				return fmt.Errorf("permission rule %q: the MCP server %q is not connected, so its tools cannot be checked now; add this rule once it is connected", r.text, provider)
			}
		case l.Shared && someonesTool(r.tool, box):
		case !box.offers(r.tool):
			return unknownTool(fmt.Sprintf("permission rule %q", r.text), r.tool, box)
		case r.pattern != nil && !box.hasContent(r.tool):
			return fmt.Errorf("permission rule %q: %s takes no specifier; write %q to match every call", r.text, r.tool, r.tool)
		}
	}
	return nil
}

func Validate(base []Layer, previous Settings, layer Layer, box Toolbox) error {
	if err := checkLayer(layer, &previous, box); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidSettings, layer.Of, err)
	}
	if _, err := New(append(slices.Clone(base), layer), nil, box); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSettings, err)
	}
	return nil
}

func unknownTool(what, tool string, box Toolbox) error {
	if provider, _, ok := strings.Cut(tool, ProviderSeparator); ok && !box.hasProvider(provider) {
		return fmt.Errorf("%s: tool %q is not in the tool registry, and no tool provider is named %q; if that MCP server was removed or renamed, remove or rename the rule", what, tool, provider)
	}
	return fmt.Errorf("%s: tool %q is not in the tool registry", what, tool)
}

func someonesTool(tool string, box Toolbox) bool {
	provider, _, ok := strings.Cut(tool, ProviderSeparator)
	return ok && !box.hasProvider(provider)
}

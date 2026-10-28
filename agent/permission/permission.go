package permission

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Settings = repo.PermissionSettings

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

type Checker struct {
	policy Policy
	rules  ruleSet
	tools  []tools.ToolDescriptor
}

type Layer struct {
	Of       string
	Settings Settings
}

func New(layers []Layer, allowlist []string, registry *tools.Registry) (*Checker, error) {
	all := make([]Settings, 0, len(layers))
	for _, l := range layers {
		if err := checkLayer(l.Settings, nil, registry); err != nil {
			return nil, fmt.Errorf("%s: %w", l.Of, err)
		}
		all = append(all, l.Settings)
	}
	settings := Resolve(all...)
	policy := Policy(settings.ApprovalPolicy)
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	set, err := parseRules(settings.Rules)
	if err != nil {
		return nil, err
	}
	for i, name := range allowlist {
		if slices.Contains(allowlist[:i], name) {
			return nil, fmt.Errorf("the tools allowlist names %q twice", name)
		}
		if !registry.Offers(name) && !registry.Unavailable(name) {
			return nil, fmt.Errorf("the tools allowlist: %w", unknownTool(fmt.Sprintf("tool %q", name), name, registry))
		}
	}
	visible := slices.DeleteFunc(registry.Descriptors(), func(d tools.ToolDescriptor) bool {
		return (allowlist != nil && !slices.Contains(allowlist, d.Name)) || set.removes(d.Name)
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
}

func checkLayer(s Settings, previous *Settings, registry *tools.Registry) error {
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
		case !registry.Offers(r.tool):
			return unknownTool(fmt.Sprintf("permission rule %q", r.text), r.tool, registry)
		case r.pattern != nil && !registry.HasPermissionContent(r.tool):
			return fmt.Errorf("permission rule %q: %s takes no specifier; write %q to match every call", r.text, r.tool, r.tool)
		}
	}
	return nil
}

func unknownTool(what, tool string, registry *tools.Registry) error {
	if provider, _, ok := strings.Cut(tool, tools.ProviderSeparator); ok && !registry.HasProvider(provider) {
		return fmt.Errorf("%s: tool %q is not in the tool registry, and no tool provider is named %q; if that MCP server was removed or renamed, remove or rename the rule", what, tool, provider)
	}
	return fmt.Errorf("%s: tool %q is not in the tool registry", what, tool)
}

var ErrInvalidSettings = errors.New("invalid permission settings")

func ValidateLayer(base []Layer, previous Settings, layer Layer, registry *tools.Registry) error {
	if err := checkLayer(layer.Settings, &previous, registry); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidSettings, layer.Of, err)
	}
	if _, err := New(append(slices.Clone(base), layer), nil, registry); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSettings, err)
	}
	return nil
}

func (c *Checker) Tools() []tools.ToolDescriptor {
	return slices.Clone(c.tools)
}

type Call struct {
	Tool        string
	Annotations tools.Annotations
	Content     tools.Content
	HasContent  bool
	Prepared    *tools.Prepared
}

type Verdict int

const (
	Run Verdict = iota
	Ask
	Deny
)

type Decision struct {
	Verdict Verdict
	Reason  string
	Rule    string
}

func (c *Checker) Decide(call Call, history []repo.ItemRow) Decision {
	if p := call.Prepared; p != nil && p.Escalated && !c.policy.allowsEscalation() {
		return Decision{Verdict: Deny, Reason: fmt.Sprintf("refused: under the approval policy %q nobody can approve running outside the sandbox; do the work inside the sandbox, or tell the user what needs to run outside it", c.policy)}
	}
	switch verdict, rule := c.rules.decide(call.Tool, call.Content, call.HasContent); verdict {
	case ruleDeny:
		return Decision{Verdict: Deny, Rule: rule, Reason: fmt.Sprintf("denied by the permission rule %s; do not try it again, find another way or tell the user", rule)}
	case ruleAsk:
		return Decision{Verdict: Ask, Rule: rule, Reason: fmt.Sprintf("Your permission rule %s asks before this call.", rule)}
	case ruleAllow:
		if p := call.Prepared; p == nil || !p.Escalated {
			return Decision{Verdict: Run, Rule: rule}
		}
	case ruleOpaque:
		if allowedForConversation(call, history) {
			return Decision{Verdict: Run, Reason: "allowed for this conversation earlier"}
		}
		return Decision{Verdict: Ask, Reason: "This command cannot be fully checked against your permission rules for " + call.Tool + ", so it needs your approval."}
	}
	if allowedForConversation(call, history) {
		return Decision{Verdict: Run, Reason: "allowed for this conversation earlier"}
	}
	if p := call.Prepared; p != nil && p.Escalated {
		return Decision{Verdict: Ask, Reason: "Runs outside the sandbox: " + p.Justification}
	}
	if c.policy.requiresApproval(call.Annotations, call.Prepared) {
		return Decision{Verdict: Ask}
	}
	return Decision{Verdict: Run}
}

func (d Decision) Approval(id string, call Call, at time.Time) *repo.Approval {
	a := &repo.Approval{ID: id, RequestedAt: at, Reason: d.Reason, Rule: d.Rule}
	if call.HasContent {
		a.Content = call.Content.Text
	}
	return a
}

func allowedForConversation(call Call, history []repo.ItemRow) bool {
	for _, it := range history {
		if !it.IsCall() || it.Tool != call.Tool || it.Approval == nil || it.Approval.Decision != repo.DecisionAcceptForSession {
			continue
		}
		if !reflect.DeepEqual(it.PreparedExecution, call.Prepared) {
			continue
		}
		if !call.HasContent || it.Approval.Content == call.Content.Text {
			return true
		}
	}
	return false
}

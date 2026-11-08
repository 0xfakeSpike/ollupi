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
	// Shared settings serve users with different tools, so they may name tools this user lacks.
	Shared bool
}

func New(layers []Layer, allowlist []string, registry *tools.Registry) (*Checker, error) {
	all := make([]Settings, 0, len(layers))
	shared := false
	for _, l := range layers {
		shared = shared || l.Shared
		if err := checkLayer(l, nil, registry); err != nil {
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
		if !registry.Offers(name) && !registry.Unavailable(name) && !(shared && someonesTool(name, registry)) {
			return nil, fmt.Errorf("the tools allowlist: %w", unknownTool(fmt.Sprintf("tool %q", name), name, registry))
		}
	}
	visible := slices.DeleteFunc(registry.Descriptors(), func(d tools.ToolDescriptor) bool {
		return (allowlist != nil && !slices.Contains(allowlist, d.Name)) || set.removes(d.Name)
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
}

// someonesTool says a tool belongs to a provider this user lacks, which another user may have.
func someonesTool(tool string, registry *tools.Registry) bool {
	provider, _, ok := strings.Cut(tool, tools.ProviderSeparator)
	return ok && !registry.HasProvider(provider)
}

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

func unknownTool(what, tool string, registry *tools.Registry) error {
	if provider, _, ok := strings.Cut(tool, tools.ProviderSeparator); ok && !registry.HasProvider(provider) {
		return fmt.Errorf("%s: tool %q is not in the tool registry, and no tool provider is named %q; if that MCP server was removed or renamed, remove or rename the rule", what, tool, provider)
	}
	return fmt.Errorf("%s: tool %q is not in the tool registry", what, tool)
}

var ErrInvalidSettings = errors.New("invalid permission settings")

func ValidateLayer(base []Layer, previous Settings, layer Layer, registry *tools.Registry) error {
	if err := checkLayer(layer, &previous, registry); err != nil {
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
	Verdict   Verdict
	Reason    string
	Rule      string
	Suggested []string
}

func (c *Checker) Decide(call Call, history []repo.ItemRow) Decision {
	d := c.decide(call, history)
	if d.Verdict == Ask && d.Rule == "" {
		d.Suggested = suggest(call)
	}
	return d
}

func (c *Checker) decide(call Call, history []repo.ItemRow) Decision {
	// Deny and ask rules speak for the user at every level, so they are read before the policy.
	switch verdict, rule := c.rules.decide(call.Tool, call.Content, call.HasContent); verdict {
	case ruleDeny:
		return Decision{Verdict: Deny, Rule: rule, Reason: fmt.Sprintf("denied by the permission rule %s; do not try it again, find another way or tell the user", rule)}
	case ruleAsk:
		return Decision{Verdict: Ask, Rule: rule, Reason: fmt.Sprintf("Your permission rule %s asks before this call.", rule)}
	case ruleAllow:
		if !c.leavesTheSandbox(call) {
			return Decision{Verdict: Run, Rule: rule}
		}
	case ruleOpaque:
		if allowedForConversation(call, history) {
			return Decision{Verdict: Run, Reason: "allowed for this conversation earlier"}
		}
		// Nobody can be asked under "never", so a command a deny rule might name is refused rather than run unread,
		// and one only an ask rule might name still asks: the user's rules hold whatever the policy.
		if c.policy == Never {
			if rule != "" {
				return Decision{Verdict: Deny, Rule: rule, Reason: fmt.Sprintf("refused: this command cannot be checked against the permission rule %s, and no one is asked under this approval policy; write the command plainly so the rules can read it, or tell the user", rule)}
			}
			return Decision{Verdict: Ask, Reason: "This command cannot be fully checked against your permission rules for " + call.Tool + ", so it needs your approval."}
		}
		if c.leavesTheSandbox(call) {
			return Decision{Verdict: Ask, Reason: "Runs outside the sandbox: " + call.Prepared.Justification}
		}
		return Decision{Verdict: Ask, Reason: "This command cannot be fully checked against your permission rules for " + call.Tool + ", so it needs your approval."}
	}
	// Nobody is asked under a policy that never asks, so what the rules left undecided runs, the sandbox's edge included.
	if c.policy == Never {
		return Decision{Verdict: Run}
	}
	if allowedForConversation(call, history) {
		return Decision{Verdict: Run, Reason: "allowed for this conversation earlier"}
	}
	if c.leavesTheSandbox(call) {
		return Decision{Verdict: Ask, Reason: "Runs outside the sandbox: " + call.Prepared.Justification}
	}
	if c.policy.requiresApproval(call.Annotations, call.Prepared) {
		return Decision{Verdict: Ask}
	}
	return Decision{Verdict: Run}
}

// leavesTheSandbox says the call was prepared to run outside the sandbox, which someone has to agree to before it runs.
// The policy that never asks has no one to agree, so there the command runs as prepared.
func (c *Checker) leavesTheSandbox(call Call) bool {
	return c.policy != Never && call.Prepared != nil && call.Prepared.Escalated
}

func (d Decision) Approval(id string, call Call, at time.Time) *repo.Approval {
	a := &repo.Approval{ID: id, RequestedAt: at, Reason: d.Reason, Rule: d.Rule, Suggested: d.Suggested}
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

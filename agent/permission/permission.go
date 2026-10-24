package permission

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
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

func New(settings Settings, allowlist []string, registry *tools.Registry) (*Checker, error) {
	policy := Policy(settings.ApprovalPolicy)
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	set, err := parseRules(settings.Rules)
	if err != nil {
		return nil, err
	}
	catalog := registry.Descriptors()
	offered := func(name string) bool {
		return slices.ContainsFunc(catalog, func(d tools.ToolDescriptor) bool { return d.Name == name })
	}
	for _, r := range set.all() {
		if !offered(r.tool) {
			return nil, fmt.Errorf("permission rule %q: tool %q is not in the tool registry", r.text, r.tool)
		}
		if r.pattern != nil && !registry.HasPermissionContent(r.tool) {
			return nil, fmt.Errorf("permission rule %q: %s takes no specifier; write %q to match every call", r.text, r.tool, r.tool)
		}
	}
	for i, name := range allowlist {
		if slices.Contains(allowlist[:i], name) {
			return nil, fmt.Errorf("tool %q is listed twice", name)
		}
		if !offered(name) {
			return nil, fmt.Errorf("tool %q is not in the tool registry", name)
		}
	}
	visible := slices.DeleteFunc(slices.Clone(catalog), func(d tools.ToolDescriptor) bool {
		return (allowlist != nil && !slices.Contains(allowlist, d.Name)) || set.removes(d.Name)
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
}

var ErrInvalidSettings = errors.New("invalid permission settings")

func ValidateLayer(base, layer Settings, registry *tools.Registry) error {
	if layer.ApprovalPolicy != "" {
		if err := Policy(layer.ApprovalPolicy).Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSettings, err)
		}
	}
	if _, err := New(Resolve(base, layer), nil, registry); err != nil {
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

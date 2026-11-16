package permission

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Settings = db.PermissionSettings

type Checker struct {
	policy Policy
	rules  ruleSet
	tools  []tools.ToolDescriptor
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
	set, _ := parseRules(settings.Rules) // the rules of the layers, each of which checkLayer parsed
	for i, name := range allowlist {
		if slices.Contains(allowlist[:i], name) {
			return nil, fmt.Errorf("the tools allowlist names %q twice", name)
		}
		if !registry.Offers(name) && !registry.Unavailable(name) && !(shared && someonesTool(name, registry)) {
			return nil, fmt.Errorf("the tools allowlist: %w", unknownTool(fmt.Sprintf("tool %q", name), name, registry))
		}
	}
	visible := slices.DeleteFunc(registry.Descriptors(), func(d tools.ToolDescriptor) bool {
		return (allowlist != nil && !slices.Contains(allowlist, d.Name)) || set.removes(d.Name) ||
			slices.ContainsFunc(layers, func(l Layer) bool { return !l.Keeps(d) })
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
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

func (c *Checker) Decide(call Call, history []db.ItemRow) Decision {
	d := c.decide(call, history)
	if d.Verdict == Ask && d.Rule == "" {
		d.Suggested = suggest(call)
	}
	return d
}

func (c *Checker) decide(call Call, history []db.ItemRow) Decision {
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
			return Decision{Verdict: Ask}
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
		return Decision{Verdict: Ask}
	}
	if c.policy.requiresApproval(call.Annotations, call.Prepared) {
		return Decision{Verdict: Ask}
	}
	return Decision{Verdict: Run}
}

// leavesTheSandbox says the call was prepared to run outside the sandbox, which someone has to agree to before it runs.
// The policy that never asks has no one to agree, so there the command runs as prepared. Its approval gives no reason:
// the prepared execution says the command escalates and why, and each client words that for its user.
func (c *Checker) leavesTheSandbox(call Call) bool {
	return c.policy != Never && call.Prepared != nil && call.Prepared.Escalated
}

func (d Decision) Approval(id string, call Call, at time.Time) *db.Approval {
	a := &db.Approval{ID: id, RequestedAt: at, Reason: d.Reason, Rule: d.Rule, Suggested: d.Suggested}
	if call.HasContent {
		a.Content = call.Content.Text
	}
	return a
}

func allowedForConversation(call Call, history []db.ItemRow) bool {
	for _, it := range history {
		if !it.IsCall() || it.Tool != call.Tool || it.Approval == nil || it.Approval.Decision != db.DecisionAcceptForSession {
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

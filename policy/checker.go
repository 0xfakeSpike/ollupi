package policy

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/conversation"
)

type Checker struct {
	policy Policy
	rules  ruleSet
	tools  []Tool
}

// New stacks the layers, the later deciding over the earlier. allowlist, unless nil, is every tool the deployment
// lets be offered.
func New(layers []Layer, allowlist []string, box Toolbox) (*Checker, error) {
	all := make([]Settings, 0, len(layers))
	shared := false
	for _, l := range layers {
		shared = shared || l.Shared
		if err := checkLayer(l, nil, box); err != nil {
			return nil, fmt.Errorf("%s: %w", l.Of, err)
		}
		all = append(all, l.Settings)
	}
	settings := Resolve(all...)
	policy := Policy(settings.ApprovalPolicy)
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	set, _ := parseRules(settings.Rules) // checkLayer parsed each layer's rules
	for i, name := range allowlist {
		if slices.Contains(allowlist[:i], name) {
			return nil, fmt.Errorf("the tools allowlist names %q twice", name)
		}
		if !box.offers(name) && !box.unavailable(name) && !(shared && someonesTool(name, box)) {
			return nil, fmt.Errorf("the tools allowlist: %w", unknownTool(fmt.Sprintf("tool %q", name), name, box))
		}
	}
	visible := slices.DeleteFunc(slices.Clone(box.Tools), func(t Tool) bool {
		return (allowlist != nil && !slices.Contains(allowlist, t.Name)) || set.removes(t.Name) ||
			slices.ContainsFunc(layers, func(l Layer) bool { return !l.Keeps(t) })
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
}

// Tools are those the layers let be offered.
func (c *Checker) Tools() []Tool {
	return slices.Clone(c.tools)
}

func (c *Checker) Decide(call Call, history conversation.View) Decision {
	d := c.decide(call, history)
	if d.Verdict == Ask && d.Rule == "" {
		d.Suggested = suggest(call)
	}
	return d
}

func (c *Checker) decide(call Call, history conversation.View) Decision {
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
	if c.leavesTheSandbox(call) || c.requiresApproval(call) {
		return Decision{Verdict: Ask}
	}
	return Decision{Verdict: Run}
}

// The policy that never asks has no one to agree, so there the command runs as prepared. The approval gives no reason:
// the prepared execution says why it escalates, and each client words that for its user.
func (c *Checker) leavesTheSandbox(call Call) bool {
	return c.policy != Never && call.Escalated
}

func (c *Checker) requiresApproval(call Call) bool {
	switch c.policy {
	case OnRequest:
		if call.Sandboxed && !call.Escalated {
			return false
		}
		return !call.ReadOnly
	case Untrusted:
		return !call.ReadOnly || call.OpenWorld
	}
	return false
}

func allowedForConversation(call Call, history conversation.View) bool {
	for _, it := range history.Items {
		if it.Kind != conversation.ItemToolCall || it.Call.Tool != call.Tool || it.Approval == nil || it.Approval.Decision != conversation.AcceptForSession {
			continue
		}
		if !bytes.Equal(it.Call.Prepared, call.Prepared) {
			continue
		}
		if !call.HasContent || it.Approval.Content == call.Content.Text {
			return true
		}
	}
	return false
}

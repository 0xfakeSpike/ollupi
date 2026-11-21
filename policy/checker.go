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
	set, _ := parseRules(settings.Rules)
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
			!Kept(layers, t)
	})
	return &Checker{policy: policy, rules: set, tools: visible}, nil
}

func (c *Checker) Tools() []Tool {
	return c.tools
}

func (c *Checker) Decide(call Call, history conversation.View) Decision {
	d := c.decide(call, history)
	if d.Verdict == Ask && d.Rule == "" {
		d.Suggested = suggest(call)
	}
	return d
}

func (c *Checker) decide(call Call, history conversation.View) Decision {
	switch verdict, rule := c.rules.decide(call.Tool, call.Content, call.HasContent); verdict {
	case ruleDeny:
		return Decision{Verdict: Deny, Rule: rule, Reason: fmt.Sprintf("denied by the permission rule %s; do not try it again, find another way or tell the user", rule)}
	case ruleAsk:
		return Decision{Verdict: Ask, Rule: rule, Reason: fmt.Sprintf("Your permission rule %s asks before this call.", rule)}
	case ruleEscalate:
		return Decision{Verdict: Run, Rule: rule}
	case ruleAllow:
		if !c.leavesTheSandbox(call) {
			return Decision{Verdict: Run, Rule: rule}
		}
	case ruleOpaque:
		switch {
		case allowedForConversation(call, history):
			return Decision{Verdict: Run, Reason: "allowed for this conversation earlier"}
		case c.policy == Never && rule != "":
			return Decision{Verdict: Deny, Rule: rule, Reason: fmt.Sprintf("refused: this command cannot be checked against the permission rule %s, and no one is asked under this approval policy; write the command plainly so the rules can read it, or tell the user", rule)}
		case c.leavesTheSandbox(call):
			return Decision{Verdict: Ask}
		}
		return Decision{Verdict: Ask, Reason: "This command cannot be fully checked against your permission rules for " + call.Tool + ", so it needs your approval."}
	}
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
		same := bytes.Equal(it.Call.Prepared, call.Prepared) && (!call.HasContent || it.Approval.Content == call.Content.Text)
		if same || coveredBySuggestion(call, it.Approval.ApprovalAsked) {
			return true
		}
	}
	return false
}

func coveredBySuggestion(call Call, asked conversation.ApprovalAsked) bool {
	rules := Rules{Allow: asked.Suggested}
	if asked.Escalated {
		rules = Rules{Escalate: asked.Suggested}
	}
	set, err := parseRules(rules)
	if err != nil {
		return false
	}
	verdict, _ := set.decide(call.Tool, call.Content, call.HasContent)
	return verdict == ruleEscalate || verdict == ruleAllow && !call.Escalated
}

package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/conversation"
)

type Settings struct {
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Rules          Rules  `json:"permissions"`
}

type Rules struct {
	Allow    []string `json:"allow,omitempty"`
	Escalate []string `json:"escalate,omitempty"`
	Ask      []string `json:"ask,omitempty"`
	Deny     []string `json:"deny,omitempty"`
}

type Policy string

const (
	Untrusted Policy = "untrusted"
	OnRequest Policy = "on-request"
	Never     Policy = "never"
)

var PolicyValues = []Policy{Untrusted, OnRequest, Never}

func (p Policy) Validate() error {
	if !slices.Contains(PolicyValues, p) {
		return fmt.Errorf("approval policy %q must be one of %q", p, PolicyValues)
	}
	return nil
}

const ProviderSeparator = "__"

type Tool struct {
	Name     string
	Provider string
	Content  bool
}

type Toolbox struct {
	Tools       []Tool
	Unavailable []string
}

func (b Toolbox) offers(name string) bool {
	return slices.ContainsFunc(b.Tools, func(t Tool) bool { return t.Name == name })
}

func (b Toolbox) unavailable(name string) bool {
	provider, _, ok := strings.Cut(name, ProviderSeparator)
	return ok && slices.Contains(b.Unavailable, provider)
}

func (b Toolbox) hasProvider(name string) bool {
	return slices.Contains(b.Unavailable, name) || slices.ContainsFunc(b.Tools, func(t Tool) bool { return t.Provider == name })
}

func (b Toolbox) hasContent(name string) bool {
	return slices.ContainsFunc(b.Tools, func(t Tool) bool { return t.Name == name && t.Content })
}

type Layer struct {
	Of        string
	Settings  Settings
	Shared    bool
	Providers []string
	Without   []string
}

func Kept(layers []Layer, t Tool) bool {
	for _, l := range layers {
		if !l.Keeps(t) {
			return false
		}
	}
	return true
}

func (l Layer) Keeps(t Tool) bool {
	return (l.Providers == nil || slices.Contains(l.Providers, t.Provider)) && !slices.Contains(l.Without, t.Provider)
}

func Resolve(layers ...Settings) Settings {
	var out Settings
	for _, l := range layers {
		if l.ApprovalPolicy != "" {
			out.ApprovalPolicy = l.ApprovalPolicy
		}
		out.Rules.Allow = appendNew(out.Rules.Allow, l.Rules.Allow)
		out.Rules.Escalate = appendNew(out.Rules.Escalate, l.Rules.Escalate)
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

type Content struct {
	Text   string
	Match  string
	Parts  []string
	Opaque bool
	Folded bool
}

func (c Content) parts() []string {
	if len(c.Parts) == 0 {
		return []string{c.Match}
	}
	return c.Parts
}

type Call struct {
	Tool       string
	ReadOnly   bool
	OpenWorld  bool
	Content    Content
	HasContent bool
	Sandboxed  bool
	Escalated  bool
	Prepared   json.RawMessage
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

func (d Decision) Asked(id string, call Call) conversation.ApprovalAsked {
	a := conversation.ApprovalAsked{Approval: id, Reason: d.Reason, Rule: d.Rule, Suggested: d.Suggested, Escalated: call.Escalated}
	if call.HasContent {
		a.Content = call.Content.Text
	}
	return a
}

var ErrInvalidSettings = errors.New("invalid permission settings")

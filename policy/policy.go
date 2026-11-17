// Package policy decides what a tool call may do: whether it runs, asks the user first, or is refused. It stacks the
// layers of settings a call is made under, the deployment's, the user's, the workspace's, the conversation's, a role's,
// and reads the user's rules and approval policy against the call. It does no I/O.
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
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type Policy string

// Rules come first at every level: a deny rule still denies and an ask rule still asks, whatever the policy.
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

// A tool's name is its provider's, then ProviderSeparator, then its own; the providers ollupi has itself leave theirs
// out.
const ProviderSeparator = "__"

// Tool is a tool as the rules see it.
type Tool struct {
	Name     string
	Provider string
	// Content says a rule can name the tool's calls by what they touch, as run_command(git status) does.
	Content bool
}

// Toolbox is the tools a user has now; Unavailable are the providers of theirs that cannot be reached now.
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
	Of       string
	Settings Settings
	// Shared settings serve users with different tools, so they may name tools this user lacks.
	Shared    bool
	Providers []string
	Without   []string
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

// Content is what a call touches, as its tool reads it from the arguments.
type Content struct {
	Text  string
	Match string
	Parts []string
	// Opaque content cannot be read through, as a command that runs a variable.
	Opaque bool
	// Match is lower case, as a path or a site is compared, so a rule's specifier is folded too.
	Folded bool
}

type Call struct {
	Tool       string
	ReadOnly   bool
	OpenWorld  bool
	Content    Content
	HasContent bool
	// Sandboxed and Escalated are the tool's plan for the call: it runs confined, or leaves the sandbox.
	Sandboxed bool
	Escalated bool
	// Prepared is the plan as the tool gave it, which an approval for the conversation must match to apply again.
	Prepared json.RawMessage
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

// Asked is the approval the call waits for.
func (d Decision) Asked(id string, call Call) conversation.ApprovalAsked {
	a := conversation.ApprovalAsked{Approval: id, Reason: d.Reason, Rule: d.Rule, Suggested: d.Suggested}
	if call.HasContent {
		a.Content = call.Content.Text
	}
	return a
}

var ErrInvalidSettings = errors.New("invalid permission settings")

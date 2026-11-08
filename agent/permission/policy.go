package permission

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/tools"
)

type Policy string

// Policy says when a call asks the user before it runs. Rules come first at every level: a deny rule still denies and
// an ask rule still asks, whatever the policy.
//
//   - Untrusted runs only what changes nothing and stays on this machine; every change and every site asks.
//   - OnRequest runs what reads, and what the sandbox confines; a change outside the sandbox, and a command prepared to
//     leave it, asks first.
//   - Never asks nothing: what the rules leave undecided runs, a command that leaves the sandbox included. A command
//     the rules cannot read is not undecided when a deny or ask rule might name it: it is refused, or asked about.
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

func (p Policy) requiresApproval(a tools.Annotations, prepared *tools.Prepared) bool {
	switch p {
	case Never:
		return false
	case OnRequest:
		if prepared != nil && prepared.Sandboxed && !prepared.Escalated {
			return false
		}
		return !a.ReadOnlyHint
	case Untrusted:
		return !a.ReadOnlyHint || tools.OpenWorld(a)
	default:
		panic(fmt.Sprintf("permission: unparsed approval policy %q", p))
	}
}

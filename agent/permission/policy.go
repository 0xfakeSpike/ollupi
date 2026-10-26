package permission

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/tools"
)

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

func (p Policy) allowsEscalation() bool { return p != Never }

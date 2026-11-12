package plan

import "testing"

func TestAPlanHasAtMostOneStepInProgress(t *testing.T) {
	ok := Plan{Steps: []Step{{Step: "read", Status: Completed}, {Step: "write", Status: InProgress}, {Step: "test", Status: Pending}}}
	if err := ok.validate(); err != nil {
		t.Fatalf("a plan in order: %v", err)
	}
	for name, p := range map[string]Plan{
		"no steps":          {},
		"two in progress":   {Steps: []Step{{Step: "a", Status: InProgress}, {Step: "b", Status: InProgress}}},
		"a blank step":      {Steps: []Step{{Step: " ", Status: Pending}}},
		"an unknown status": {Steps: []Step{{Step: "a", Status: "done"}}},
	} {
		if err := p.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

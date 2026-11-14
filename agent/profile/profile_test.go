package profile

import "testing"

func TestASmallInputGetsTheCompactProfile(t *testing.T) {
	if p := For(compactBelow - 1); p.Core == nil || p.Skills {
		t.Errorf("a small input: %+v", p)
	}
	if p := For(compactBelow); p.Core != nil || !p.Skills || p.Rules != fullRules {
		t.Errorf("an input that holds the full prompt: %+v", p)
	}
}

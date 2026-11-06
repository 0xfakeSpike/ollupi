package llm

import "testing"

func TestLimitsInput(t *testing.T) {
	cases := map[string]struct {
		limits Limits
		want   int
	}{
		"window less the answer":    {Limits{Context: 1000, Output: 100}, 900},
		"a cap below that room":     {Limits{Context: 1000, Output: 100, MaxInput: 500}, 500},
		"a cap the window is under": {Limits{Context: 131072, Output: 16384, MaxInput: 130048}, 114688},
	}
	for name, c := range cases {
		if got := c.limits.Input(); got != c.want {
			t.Errorf("%s: Input() = %d, want %d", name, got, c.want)
		}
	}
}

func TestLimitsCheck(t *testing.T) {
	for _, l := range []Limits{{}, {Context: 100, Output: 100}, {Context: 1000, Output: 100, MaxInput: -1}} {
		if l.check() == nil {
			t.Errorf("%+v passed", l)
		}
	}
	if err := (Limits{Context: 1000, Output: 100, MaxInput: 500}).check(); err != nil {
		t.Error(err)
	}
}

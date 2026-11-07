package llm

import "testing"

func TestLimitsInput(t *testing.T) {
	cases := map[string]struct {
		limits Limits
		want   int
	}{
		"window less the answer":          {Limits{Context: 1000, Output: 100}, 900},
		"a cap below that room":           {Limits{Context: 1000, Output: 100, MaxInput: 500}, 500},
		"a cap the window is under":       {Limits{Context: 131072, Output: 16384, MaxInput: 130048}, 114688},
		"a long answer keeps a quarter":   {Limits{Context: 1048576, Output: 393216}, 786432},
		"an answer as long as the window": {Limits{Context: 1000, Output: 1000}, 750},
	}
	for name, c := range cases {
		if got := c.limits.Input(); got != c.want {
			t.Errorf("%s: Input() = %d, want %d", name, got, c.want)
		}
	}
}

func TestLimitsCheck(t *testing.T) {
	for _, l := range []Limits{{}, {Context: 100}, {Output: 100}, {Context: 1000, Output: 100, MaxInput: -1}} {
		if l.check() == nil {
			t.Errorf("%+v passed", l)
		}
	}
	for _, l := range []Limits{{Context: 1000, Output: 100, MaxInput: 500}, {Context: 1000, Output: 1000}} {
		if err := l.check(); err != nil {
			t.Error(err)
		}
	}
}

func TestLimitsAnswer(t *testing.T) {
	long := Limits{Context: 200_000, Output: 128_000}
	short := Limits{Context: 200_000, Output: 32_000}
	cases := []struct {
		limits Limits
		input  int
		want   int
	}{
		{long, 0, 128_000},
		{long, 100_000, 100_000},
		{long, 150_000, 50_000},
		{long, 190_000, 50_000},
		{short, 190_000, 32_000},
		{Limits{Context: 1000, Output: 1000}, 0, 1000},
	}
	for _, c := range cases {
		if got := c.limits.Answer(c.input); got != c.want {
			t.Errorf("%+v.Answer(%d) = %d, want %d", c.limits, c.input, got, c.want)
		}
	}
}

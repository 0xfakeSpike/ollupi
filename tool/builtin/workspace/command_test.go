package workspace

import (
	"slices"
	"testing"
)

// A shell's script is read for the commands it runs when every option before it takes no argument. After any other
// option the next word may be that option's, so the command is opaque: a deny or ask rule that might name what it
// runs is not passed by.
func TestAShellsScriptIsReadOrOpaque(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		parts  []string
		opaque bool
	}{
		{args: []string{"-c", "rm -rf x"}, parts: []string{"rm -rf x"}},
		{args: []string{"-lc", "rm -rf x"}, parts: []string{"rm -rf x"}},
		{args: []string{"-e", "-c", "rm -rf x"}, parts: []string{"rm -rf x"}},
		{args: []string{"-o", "pipefail", "-c", "rm -rf x"}, opaque: true},
		{args: []string{"-O", "extglob", "-c", "rm -rf x"}, opaque: true},
		{args: []string{"-C", "rm -rf x", "-c", "true"}, opaque: true},
		{args: []string{"--norc", "-c", "rm -rf x"}, opaque: true},
	} {
		c := contentOf("bash", tc.args)
		if !slices.Equal(c.Parts, tc.parts) || c.Opaque != tc.opaque {
			t.Errorf("bash %q: parts %q, opaque %v; want %q, %v", tc.args, c.Parts, c.Opaque, tc.parts, tc.opaque)
		}
	}
}

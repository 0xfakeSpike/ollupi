package workspace

import (
	"slices"
	"testing"
)

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

func TestAScriptSplitsIntoItsCommandsOrIsOpaque(t *testing.T) {
	for _, tc := range []struct {
		script   string
		commands [][]string
		opaque   bool
	}{
		{script: "ls; rm x", commands: [][]string{{"ls"}, {"rm", "x"}}},
		{script: "npm run build&&npm test || cat log|wc -l", commands: [][]string{{"npm", "run", "build"}, {"npm", "test"}, {"cat", "log"}, {"wc", "-l"}}},
		{script: `git commit -m "fix: a; b"`, commands: [][]string{{"git", "commit", "-m", "fix: a; b"}}},
		{script: `ec\ho 'a b' "c\"d" \;`, commands: [][]string{{"echo", "a b", `c"d`, ";"}}},
		{script: "ls 2>&1 > out.txt", commands: [][]string{{"ls"}}},
		{script: "ls \\\n  -la # list", commands: [][]string{{"ls", "-la"}}},
		{script: "cat > f <<'EOF'\n$(rm x)\nEOF", commands: [][]string{{"cat"}}},
		{script: "cat <<EOF\n$(rm x)\nEOF", commands: [][]string{{"cat"}, {"rm", "x"}}, opaque: true},
		{script: "echo $(rm x)", commands: [][]string{{"echo", "$(rm x)"}, {"rm", "x"}}, opaque: true},
		{script: `echo "$HOME"`, commands: [][]string{{"echo", `"$HOME"`}}, opaque: true},
		{script: "(cd x; rm y)", commands: [][]string{{"cd", "x"}, {"rm", "y"}}, opaque: true},
		{script: "if true; then rm x; fi", commands: [][]string{{"true"}, {"rm", "x"}}, opaque: true},
		{script: "FOO=1 make", commands: [][]string{{"make"}}, opaque: true},
		{script: "cat <(ls)", commands: [][]string{{"cat", "<(ls)"}, {"ls"}}, opaque: true},
		{script: "echo ~/x a{b,c} 中文", commands: [][]string{{"echo", "~/x", "a{b,c}", "中文"}}, opaque: true},
		{script: "! true", commands: [][]string{{"true"}}, opaque: true},
		{script: "echo 'unterminated", opaque: true},
	} {
		commands, opaque := splitScript(tc.script)
		if !slices.EqualFunc(commands, tc.commands, slices.Equal[[]string]) || opaque != tc.opaque {
			t.Errorf("%q: %q, opaque %v; want %q, %v", tc.script, commands, opaque, tc.commands, tc.opaque)
		}
	}
}

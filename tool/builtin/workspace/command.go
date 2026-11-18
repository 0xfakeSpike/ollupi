package workspace

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"mvdan.cc/sh/v3/syntax"
)

const maxCommandDepth = 4

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	errProgram = errors.New(`workspace: argument "program" must be non-empty string`)
)

func permissionContent(_ context.Context, call tool.Call) (policy.Content, error) {
	a, err := commandOf(call.Arguments)
	if err != nil {
		return policy.Content{}, err
	}
	c := contentOf(a.Program, a.Args)
	c.Opaque = c.Opaque || len(a.Env) > 0
	words := make([]string, 0, len(a.Env)+1+len(a.Args))
	for _, k := range slices.Sorted(maps.Keys(a.Env)) {
		words = append(words, k+"="+a.Env[k])
	}
	c.Text = strings.Join(append(append(words, a.Program), a.Args...), " ")
	return c, nil
}

func contentOf(program string, args []string) policy.Content {
	words := append([]string{program}, args...)
	c := policy.Content{Text: strings.Join(words, " "), Match: line(words)}
	parts, opaque := expand(words, 0)
	c.Opaque = opaque
	if len(parts) != 1 || parts[0] != c.Match {
		c.Parts = parts
	}
	return c
}

func line(words []string) string {
	return strings.Join(append([]string{programName(words[0])}, words[1:]...), " ")
}

func programName(word string) string {
	return strings.ToLower(filepath.Base(word))
}

var systemDirs = []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/local/bin", "/opt/homebrew/bin", "/opt/homebrew/sbin"}

func trustedProgram(word string) bool {
	if !strings.Contains(word, "/") {
		return true
	}
	dir := filepath.Dir(filepath.Clean(word))
	for _, d := range systemDirs {
		if strings.EqualFold(dir, d) {
			return true
		}
	}
	return false
}

func expand(words []string, depth int) ([]string, bool) {
	if len(words) == 0 {
		return nil, false
	}
	whole := []string{line(words)}
	if depth > maxCommandDepth || !trustedProgram(words[0]) {
		return whole, true
	}
	name := programName(words[0])
	switch {
	case isShell(name):
		script, ok, opaque := shellScript(words[1:])
		if opaque {
			return whole, true
		}
		if !ok {
			return whole, false
		}
		commands, opaque := splitScript(script)
		var parts []string
		for _, cmd := range commands {
			if assignment.MatchString(cmd[0]) || reserved(cmd[0]) || strings.ContainsAny(cmd[0], "*?[]=") {
				opaque = true
			}
			sub, subOpaque := expand(cmd, depth+1)
			parts, opaque = append(parts, sub...), opaque || subOpaque
		}
		return parts, opaque
	case isWrapper(name):
		rest, ok := unwrap(name, words[1:])
		if !ok || len(rest) == 0 {
			return whole, true
		}
		parts, opaque := expand(rest, depth+1)
		return parts, opaque
	case reserved(name):
		return whole, true
	}
	return whole, false
}

const shellFlags = "aehiklmnuvx"

func shellScript(args []string) (string, bool, bool) {
	takesC := false
	for _, a := range args {
		switch {
		case len(a) > 1 && a[0] == '-' && strings.Trim(a[1:], shellFlags+"c") == "":
			if strings.ContainsRune(a[1:], 'c') {
				takesC = true
			}
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			return "", false, true
		default:
			return a, takesC, false
		}
	}
	return "", false, takesC
}

func isWrapper(name string) bool {
	switch name {
	case "env", "nohup", "nice", "timeout", "xargs", "sudo", "doas", "caffeinate", "time":
		return true
	}
	return false
}

func unwrap(name string, args []string) ([]string, bool) {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return nil, false
	}
	if name == "env" && len(args) > 0 && assignment.MatchString(args[0]) {
		return nil, false
	}
	if name == "xargs" || name == "sudo" || name == "doas" {
		return nil, false
	}
	if name == "timeout" {
		if len(args) < 2 {
			return nil, false
		}
		return args[1:], true
	}
	return args, true
}

func reserved(word string) bool {
	switch strings.ToLower(word) {
	case "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done", "case", "esac", "select",
		"function", "coproc", "time", "in", "{", "}", "!", "[[", "]]",
		"eval", "source", ".", "exec", "command", "builtin", "alias", "trap", "declare", "typeset", "export", "local", "readonly", "set", "shopt", "enable", "unset":
		return true
	}
	return false
}

func splitScript(script string) ([][]string, bool) {
	f, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, true
	}
	var commands [][]string
	opaque := false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.File, *syntax.BinaryCmd, *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, nil:
		case *syntax.Stmt:
			opaque = opaque || n.Negated || n.Coprocess || n.Disown
		case *syntax.CallExpr:
			var words []string
			for _, w := range n.Args {
				word, ok := literal(w.Parts, false)
				if !ok {
					word, opaque = source(w), true
				}
				words = append(words, word)
			}
			if len(words) > 0 {
				commands = append(commands, words)
			}
		case *syntax.Redirect:
			_, plainTarget := literal(wordParts(n.Word), false)
			_, plainBody := literal(wordParts(n.Hdoc), true)
			plainFD := n.N == nil || isDigits(n.N.Value)
			opaque = opaque || !plainTarget || !plainBody || !plainFD
		default:
			opaque = true
		}
		return true
	})
	return commands, opaque
}

func wordParts(w *syntax.Word) []syntax.WordPart {
	if w == nil {
		return nil
	}
	return w.Parts
}

func source(w *syntax.Word) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, w)
	return b.String()
}

func literal(parts []syntax.WordPart, quoted bool) (string, bool) {
	var b strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if !quoted && strings.ContainsFunc(p.Value, unsettled) {
				return "", false
			}
			b.WriteString(unescape(p.Value, quoted))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			inner, ok := literal(p.Parts, true)
			if p.Dollar || !ok {
				return "", false
			}
			b.WriteString(inner)
		default:
			return "", false
		}
	}
	return b.String(), true
}

func unescape(s string, quoted bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		escapes := s[i] == '\\' && i+1 < len(s) && (!quoted || strings.IndexByte("$`\"\\", s[i+1]) >= 0)
		if escapes {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unsettled(r rune) bool {
	return r > 127 || strings.ContainsRune("{}~!#", r)
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func blacklisted(c policy.Content, blocked string) bool {
	want := strings.Fields(blocked)
	if len(want) == 0 {
		return false
	}
	for _, entry := range append([]string{c.Match}, c.Parts...) {
		words := strings.Fields(entry)
		for i := 0; i+len(want) <= len(words); i++ {
			if startsWith(words[i:], want) {
				return true
			}
		}
	}
	return false
}

func startsWith(words, want []string) bool {
	got, program := words[0], want[0]
	sameProgram := strings.EqualFold(got, programName(program)) ||
		!strings.Contains(program, "/") && strings.EqualFold(programName(got), programName(program))
	return sameProgram && slices.Equal(words[1:len(want)], want[1:])
}

func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ash", "ksh", "fish", "csh", "tcsh":
		return true
	}
	return false
}

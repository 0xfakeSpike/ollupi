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
)

const maxCommandDepth = 4

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	errProgram = errors.New(`workspace: argument "program" must be non-empty string`)
)

func programOf(arguments map[string]any) (string, error) {
	program, _ := arguments["program"].(string)
	if program = strings.TrimSpace(program); program == "" {
		return "", errProgram
	}
	return program, nil
}

func permissionContent(_ context.Context, call tool.Call) (policy.Content, error) {
	arguments := call.Arguments
	program, err := programOf(arguments)
	if err != nil {
		return policy.Content{}, err
	}
	args := parseExecArgsSlice(arguments["args"])
	env, err := commandEnvironment(arguments["env"])
	if err != nil {
		return policy.Content{}, err
	}
	c := contentOf(program, args)
	c.Opaque = c.Opaque || len(env) > 0
	// The approval shows the environment first, as the command line would.
	words := make([]string, 0, len(env)+1+len(args))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		words = append(words, k+"="+env[k])
	}
	c.Text = strings.Join(append(append(words, program), args...), " ")
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

// shellFlags are the one-letter options that take no argument in any of the shells: after any other option the next
// word may be the option's own and not the script, so the command is not read.
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
	var (
		commands [][]string
		words    []string
		word     strings.Builder
		inWord   bool
		single   bool
		double   bool
		opaque   bool
		skipNext bool
	)
	endWord := func() {
		if !inWord {
			return
		}
		if skipNext {
			skipNext = false
		} else {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			commands = append(commands, words)
		}
		words = nil
	}
	runes := []rune(script)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case single:
			if r == '\'' {
				single = false
			} else {
				word.WriteRune(r)
			}
		case double:
			switch {
			case r == '"':
				double = false
			case r == '$' || r == '`':
				opaque = true
				word.WriteRune(r)
			case r == '\\' && i+1 < len(runes):
				i++
				if runes[i] == '\n' {
					opaque = true
				}
				if !strings.ContainsRune("$`\"\\", runes[i]) {
					word.WriteRune('\\')
				}
				word.WriteRune(runes[i])
			default:
				word.WriteRune(r)
			}
		case r == '\'':
			single, inWord = true, true
		case r == '"':
			double, inWord = true, true
		case r == '\\':
			if i+1 >= len(runes) || runes[i+1] == '\n' {
				opaque = true
				continue
			}
			i++
			word.WriteRune(runes[i])
			inWord = true
		case r == '<' || r == '>':
			if inWord && isDigits(word.String()) {
				word.Reset()
				inWord = false
			}
			endWord()
			if i+1 < len(runes) && runes[i+1] == '(' {
				opaque = true
			}
			for i+1 < len(runes) && strings.ContainsRune("<>&|", runes[i+1]) {
				i++
			}
			skipNext = true
		case r == ';' || r == '&' || r == '|' || r == '\n':
			endCommand()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		case r > 127 || strings.ContainsRune("$`(){}#!~", r):
			opaque = true
			word.WriteRune(r)
			inWord = true
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endCommand()
	if single || double || skipNext {
		opaque = true
	}
	return commands, opaque
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func blacklisted(c policy.Content, blocked string) bool {
	want := strings.Fields(blocked)
	if len(want) == 0 {
		return false
	}
	for _, entry := range append([]string{c.Match}, c.Parts...) {
		words := strings.Fields(entry)
		for i := 0; i+len(want) <= len(words); i++ {
			match := true
			for j, w := range want {
				got := words[i+j]
				if j == 0 {
					if !strings.EqualFold(got, programName(w)) && (strings.Contains(w, "/") || !strings.EqualFold(programName(got), programName(w))) {
						match = false
						break
					}
					continue
				}
				if got != w {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ash", "ksh", "fish", "csh", "tcsh":
		return true
	}
	return false
}

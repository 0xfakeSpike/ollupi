package shell

import (
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/tools"
)

const maxCommandDepth = 4

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	errProgram = errors.New(`shell: argument "program" must be non-empty string`)
)

func permissionContent(arguments map[string]any) (tools.Content, error) {
	text, err := commandLine(arguments)
	if err != nil {
		return tools.Content{}, err
	}
	program, _ := arguments["program"].(string)
	args, err := parseExecArgsSlice(arguments["args"])
	if err != nil {
		return tools.Content{}, err
	}
	env, err := commandEnvironment(arguments["env"])
	if err != nil {
		return tools.Content{}, err
	}
	c := contentOf(strings.TrimSpace(program), args)
	c.Text = text
	c.Opaque = c.Opaque || len(env) > 0
	return c, nil
}

func commandLine(arguments map[string]any) (string, error) {
	program, ok := arguments["program"].(string)
	if !ok || strings.TrimSpace(program) == "" {
		return "", errProgram
	}
	args, err := parseExecArgsSlice(arguments["args"])
	if err != nil {
		return "", err
	}
	env, err := commandEnvironment(arguments["env"])
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	words := make([]string, 0, len(keys)+1+len(args))
	for _, k := range keys {
		words = append(words, k+"="+env[k])
	}
	words = append(words, strings.TrimSpace(program))
	return strings.Join(append(words, args...), " "), nil
}

func contentOf(program string, args []string) tools.Content {
	words := append([]string{program}, args...)
	c := tools.Content{Text: strings.Join(words, " "), Match: line(words)}
	parts, opaque := expand(words, 0)
	c.Opaque = opaque
	if len(parts) != 1 || parts[0] != c.Match {
		c.Parts = parts
	}
	return c
}

func line(words []string) string {
	return strings.Join(append([]string{filepath.Base(words[0])}, words[1:]...), " ")
}

func expand(words []string, depth int) ([]string, bool) {
	if len(words) == 0 {
		return nil, false
	}
	if depth > maxCommandDepth {
		return []string{line(words)}, true
	}
	base := filepath.Base(words[0])
	switch {
	case isShell(base):
		script, ok := shellScript(words[1:])
		if !ok {
			return []string{line(words)}, false
		}
		commands, opaque := splitScript(script)
		var parts []string
		for _, cmd := range commands {
			for len(cmd) > 0 && assignment.MatchString(cmd[0]) {
				cmd, opaque = cmd[1:], true
			}
			if len(cmd) == 0 {
				continue
			}
			if strings.ContainsAny(cmd[0], "$*?[") {
				opaque = true
			}
			sub, subOpaque := expand(cmd, depth+1)
			parts, opaque = append(parts, sub...), opaque || subOpaque
		}
		return parts, opaque
	case isWrapper(base):
		rest, opaque := unwrap(base, words[1:])
		if len(rest) == 0 {
			return []string{line(words)}, opaque
		}
		parts, subOpaque := expand(rest, depth+1)
		return parts, opaque || subOpaque
	case base == "eval" || base == "source" || base == ".":
		return []string{line(words)}, true
	}
	return []string{line(words)}, false
}

func shellScript(args []string) (string, bool) {
	takesC := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			continue
		case a == "-o" || a == "+o" || a == "-O" || a == "+O":
			i++
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			if strings.ContainsRune(a[1:], 'c') {
				takesC = true
			}
		default:
			return a, takesC
		}
	}
	return "", false
}

func isWrapper(base string) bool {
	switch base {
	case "env", "nohup", "time", "nice", "command", "exec", "xargs", "timeout", "caffeinate", "sudo", "doas":
		return true
	}
	return false
}

func unwrap(base string, args []string) ([]string, bool) {
	valued := map[string]bool{}
	switch base {
	case "nice":
		valued["-n"] = true
	case "xargs":
		for _, f := range []string{"-I", "-J", "-L", "-n", "-P", "-s", "-E", "-d", "-R", "-S"} {
			valued[f] = true
		}
	case "sudo", "doas":
		for _, f := range []string{"-u", "-g", "-C", "-h", "-p", "-U"} {
			valued[f] = true
		}
	case "env":
		for _, f := range []string{"-u", "-P", "-S", "-C"} {
			valued[f] = true
		}
	case "timeout":
		for _, f := range []string{"-s", "-k", "--signal", "--kill-after"} {
			valued[f] = true
		}
	}
	opaque := base == "xargs" || base == "sudo" || base == "doas"
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			i++
			return trimDuration(base, args[i:]), opaque
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if valued[a] {
				i++
			}
			if base == "env" && (a == "-S" || strings.HasPrefix(a, "--split-string")) {
				opaque = true
			}
			i++
		case base == "env" && assignment.MatchString(a):
			opaque = true
			i++
		default:
			return trimDuration(base, args[i:]), opaque
		}
	}
	return nil, opaque
}

func trimDuration(base string, args []string) []string {
	if base == "timeout" && len(args) > 0 {
		return args[1:]
	}
	return args
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
		words = dropKeywords(words)
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
			case r == '`' || (r == '$' && i+1 < len(runes) && runes[i+1] == '('):
				opaque = true
				word.WriteRune(r)
			case r == '\\' && i+1 < len(runes):
				i++
				word.WriteRune(runes[i])
			default:
				word.WriteRune(r)
			}
		case r == '\'':
			single, inWord = true, true
		case r == '"':
			double, inWord = true, true
		case r == '\\' && i+1 < len(runes):
			i++
			if runes[i] != '\n' {
				word.WriteRune(runes[i])
				inWord = true
			}
		case r == '`' || (r == '$' && i+1 < len(runes) && runes[i+1] == '('):
			opaque = true
			word.WriteRune(r)
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
		case r == ';' || r == '&' || r == '|' || r == '\n' || r == '(' || r == ')':
			endCommand()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if single || double {
		opaque = true
	}
	endCommand()
	return commands, opaque
}

func dropKeywords(words []string) []string {
	for len(words) > 0 {
		switch words[0] {
		case "if", "then", "else", "elif", "while", "until", "do", "!", "{":
			words = words[1:]
		case "for", "select", "case", "function", "fi", "done", "esac", "in", "}":
			return nil
		default:
			return words
		}
	}
	return words
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

func blacklisted(c tools.Content, blocked string) bool {
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
				if got != w && (j > 0 || strings.Contains(w, "/") || filepath.Base(got) != w) {
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

func isShell(program string) bool {
	switch strings.ToLower(filepath.Base(strings.TrimSpace(program))) {
	case "sh", "bash", "zsh", "dash", "ash", "ksh", "fish":
		return true
	}
	return false
}

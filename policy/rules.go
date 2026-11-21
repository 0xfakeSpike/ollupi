package policy

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type ruleVerdict int

const (
	ruleNone ruleVerdict = iota
	ruleDeny
	ruleAsk
	ruleAllow
	ruleEscalate
	ruleOpaque
)

type rule struct {
	text    string
	tool    string
	pattern *regexp.Regexp
	folded  *regexp.Regexp
}

func parseRule(text string) (rule, error) {
	s := strings.TrimSpace(text)
	r := rule{text: s, tool: s}
	if open := strings.IndexByte(s, '('); open >= 0 {
		if !strings.HasSuffix(s, ")") {
			return rule{}, fmt.Errorf("permission rule %q: a specifier must end with )", text)
		}
		r.tool = s[:open]
		spec := s[open+1 : len(s)-1]
		switch {
		case strings.TrimSpace(spec) == "":
			return rule{}, fmt.Errorf("permission rule %q: empty specifier; write the tool name alone to match every call", text)
		case spec != "*":
			r.folded = specifierPattern(strings.ToLower(spec))
			if first, rest, found := strings.Cut(spec, " "); found {
				spec = strings.ToLower(first) + " " + rest
			} else {
				spec = strings.ToLower(spec)
			}
			r.pattern = specifierPattern(spec)
		}
	}
	if r.tool == "" || strings.ContainsAny(r.tool, " \t()") {
		return rule{}, fmt.Errorf("permission rule %q: expected Tool or Tool(specifier)", text)
	}
	return r, nil
}

func specifierPattern(spec string) *regexp.Regexp {
	optionalTail := false
	switch {
	case strings.HasSuffix(spec, ":*"):
		spec, optionalTail = strings.TrimSuffix(spec, ":*"), true
	case strings.HasSuffix(spec, " *"):
		spec, optionalTail = strings.TrimSuffix(spec, " *"), true
	}
	parts := strings.Split(spec, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	expr := "(?s)^" + strings.Join(parts, ".*")
	if optionalTail {
		expr += "(?: .*)?"
	}
	return regexp.MustCompile(expr + "$")
}

func (r rule) matches(tool, content string, hasContent, folded bool) bool {
	if r.tool != tool {
		return false
	}
	if r.pattern == nil {
		return true
	}
	if folded {
		return hasContent && r.folded.MatchString(content)
	}
	return hasContent && r.pattern.MatchString(content)
}

type ruleSet struct {
	allow, escalate, ask, deny []rule
}

func parseRules(p Rules) (ruleSet, error) {
	deny, err := parseList("deny", p.Deny)
	if err != nil {
		return ruleSet{}, err
	}
	ask, err := parseList("ask", p.Ask)
	if err != nil {
		return ruleSet{}, err
	}
	allow, err := parseList("allow", p.Allow)
	if err != nil {
		return ruleSet{}, err
	}
	escalate, err := parseList("escalate", p.Escalate)
	if err != nil {
		return ruleSet{}, err
	}
	return ruleSet{deny: deny, ask: ask, allow: allow, escalate: escalate}, nil
}

func parseList(name string, texts []string) ([]rule, error) {
	var out []rule
	for _, text := range texts {
		r, err := parseRule(text)
		if err != nil {
			return nil, fmt.Errorf("permissions.%s: %w", name, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s ruleSet) removes(tool string) bool {
	for _, r := range s.deny {
		if r.tool == tool && r.pattern == nil {
			return true
		}
	}
	return false
}

func (s ruleSet) decide(tool string, content Content, hasContent bool) (ruleVerdict, string) {
	entries := append([]string{content.Match}, content.Parts...)
	if r, ok := firstMatch(s.deny, tool, entries, hasContent, content.Folded); ok {
		return ruleDeny, r.text
	}
	if r, ok := firstMatch(s.ask, tool, entries, hasContent, content.Folded); ok {
		return ruleAsk, r.text
	}
	if hasContent && content.Opaque {
		specified := func(r rule) bool { return r.tool == tool && r.pattern != nil }
		if i := slices.IndexFunc(s.deny, specified); i >= 0 {
			return ruleOpaque, s.deny[i].text
		}
		if slices.ContainsFunc(s.ask, specified) {
			return ruleOpaque, ""
		}
	}
	if text, ok := allows(s.escalate, tool, content, hasContent); ok {
		return ruleEscalate, text
	}
	if text, ok := allows(slices.Concat(s.allow, s.escalate), tool, content, hasContent); ok {
		return ruleAllow, text
	}
	return ruleNone, ""
}

func allows(rules []rule, tool string, content Content, hasContent bool) (string, bool) {
	for _, r := range rules {
		if r.tool == tool && r.pattern == nil {
			return r.text, true
		}
	}
	if !hasContent || content.Opaque {
		return "", false
	}
	var used []string
	for _, part := range content.parts() {
		i := slices.IndexFunc(rules, func(r rule) bool { return r.matches(tool, part, true, content.Folded) })
		if i < 0 {
			return "", false
		}
		if !slices.Contains(used, rules[i].text) {
			used = append(used, rules[i].text)
		}
	}
	return strings.Join(used, ", "), true
}

var subcommand = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var (
	sensitivePrograms = []string{"rm", "rmdir", "mv", "dd", "chmod", "chown", "sudo", "kill", "pkill", "killall", "launchctl", "crontab",
		"python", "python3", "node", "ruby", "perl", "osascript", "expect"}
	sensitiveGit = []string{"push", "reset", "clean", "checkout", "restore", "rebase"}
	codeReaders  = []string{"sh", "bash", "zsh", "dash", "python", "python3", "node", "ruby", "perl", "osascript", "expect"}
)

func sensitive(words []string) bool {
	if slices.Contains(sensitivePrograms, words[0]) {
		return true
	}
	return words[0] == "git" && len(words) > 1 && slices.Contains(sensitiveGit, words[1])
}

func suggest(call Call) []string {
	if !call.HasContent {
		return []string{call.Tool}
	}
	if call.Content.Opaque {
		return nil
	}
	var out []string
	for _, part := range call.Content.parts() {
		words := strings.Fields(part)
		if len(words) == 0 || strings.Contains(part, "*") {
			return nil
		}
		readsCode := call.Escalated && slices.Contains(codeReaders, words[0])
		if readsCode {
			return nil
		}
		spec := part
		switch {
		case len(words) == 1 || sensitive(words):
		case subcommand.MatchString(words[1]):
			spec = words[0] + " " + words[1] + ":*"
		default:
			spec = words[0] + ":*"
		}
		text := call.Tool + "(" + spec + ")"
		if r, err := parseRule(text); err != nil || !r.matches(call.Tool, part, true, call.Content.Folded) {
			return nil
		}
		if !slices.Contains(out, text) {
			out = append(out, text)
		}
	}
	return out
}

func firstMatch(rules []rule, tool string, entries []string, hasContent, folded bool) (rule, bool) {
	for _, r := range rules {
		for _, entry := range entries {
			if r.matches(tool, entry, hasContent, folded) {
				return r, true
			}
		}
	}
	return rule{}, false
}

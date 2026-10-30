package permission

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Rules = repo.PermissionRules

type ruleVerdict int

const (
	ruleNone ruleVerdict = iota
	ruleDeny
	ruleAsk
	ruleAllow
	ruleOpaque
)

type rule struct {
	text    string
	tool    string
	pattern *regexp.Regexp
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

func (r rule) matches(tool, content string, hasContent bool) bool {
	if r.tool != tool {
		return false
	}
	if r.pattern == nil {
		return true
	}
	return hasContent && r.pattern.MatchString(content)
}

type ruleSet struct {
	allow, ask, deny []rule
}

func parseRules(p Rules) (ruleSet, error) {
	var set ruleSet
	for _, list := range []struct {
		name  string
		texts []string
		into  *[]rule
	}{{"deny", p.Deny, &set.deny}, {"ask", p.Ask, &set.ask}, {"allow", p.Allow, &set.allow}} {
		for _, text := range list.texts {
			r, err := parseRule(text)
			if err != nil {
				return ruleSet{}, fmt.Errorf("permissions.%s: %w", list.name, err)
			}
			*list.into = append(*list.into, r)
		}
	}
	return set, nil
}

func (s ruleSet) all() []rule {
	return append(append(append([]rule(nil), s.deny...), s.ask...), s.allow...)
}

func (s ruleSet) removes(tool string) bool {
	for _, r := range s.deny {
		if r.tool == tool && r.pattern == nil {
			return true
		}
	}
	return false
}

func (s ruleSet) decide(tool string, content tools.Content, hasContent bool) (ruleVerdict, string) {
	entries := append([]string{content.Match}, content.Parts...)
	for _, group := range []struct {
		rules []rule
		kind  ruleVerdict
	}{{s.deny, ruleDeny}, {s.ask, ruleAsk}} {
		for _, r := range group.rules {
			for _, entry := range entries {
				if r.matches(tool, entry, hasContent) {
					return group.kind, r.text
				}
			}
		}
	}
	if hasContent && content.Opaque && slices.ContainsFunc(append(slices.Clone(s.deny), s.ask...), func(r rule) bool { return r.tool == tool && r.pattern != nil }) {
		return ruleOpaque, ""
	}
	for _, r := range s.allow {
		if r.tool == tool && r.pattern == nil {
			return ruleAllow, r.text
		}
	}
	if !hasContent || content.Opaque {
		return ruleNone, ""
	}
	if len(content.Parts) == 0 {
		content.Parts = []string{content.Match}
	}
	var used []string
	for _, part := range content.Parts {
		i := slices.IndexFunc(s.allow, func(r rule) bool { return r.matches(tool, part, true) })
		if i < 0 {
			return ruleNone, ""
		}
		if !slices.Contains(used, s.allow[i].text) {
			used = append(used, s.allow[i].text)
		}
	}
	return ruleAllow, strings.Join(used, ", ")
}

var subcommand = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func suggest(call Call) []string {
	if p := call.Prepared; p != nil && p.Escalated {
		return nil
	}
	if !call.HasContent {
		return []string{call.Tool}
	}
	if call.Content.Opaque {
		return nil
	}
	parts := call.Content.Parts
	if len(parts) == 0 {
		parts = []string{call.Content.Match}
	}
	var out []string
	for _, part := range parts {
		words := strings.Fields(part)
		if len(words) == 0 || strings.Contains(part, "*") {
			return nil
		}
		spec := part
		if len(words) > 1 && subcommand.MatchString(words[1]) {
			spec = words[0] + " " + words[1] + ":*"
		}
		text := call.Tool + "(" + spec + ")"
		if r, err := parseRule(text); err != nil || !r.matches(call.Tool, part, true) {
			return nil
		}
		if !slices.Contains(out, text) {
			out = append(out, text)
		}
	}
	return out
}

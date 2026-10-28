package permission

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

type fileTools struct{}

func (fileTools) Name() (string, string) { return "files", "Touches files." }

func (fileTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{
		{Name: "touch", InputSchema: map[string]any{"type": "object"}},
		{Name: "exec", InputSchema: map[string]any{"type": "object"}, PermissionContent: func(context.Context, map[string]any) (tools.Content, error) { return tools.Content{}, nil }},
		{Name: "click", InputSchema: map[string]any{"type": "object"}, PermissionContent: func(context.Context, map[string]any) (tools.Content, error) { return tools.Content{}, nil }},
	}, nil
}

func (fileTools) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{}, nil
}

func testRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.New(context.Background(), []string{"github"}, fileTools{})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

var defaults = Layer{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(OnRequest)}}

func layer(allow ...string) Layer {
	return Layer{Of: "the workspace", Settings: Settings{Rules: Rules{Allow: allow}}}
}

func TestTurnKeepsRulesForUnavailableProviders(t *testing.T) {
	registry := testRegistry(t)
	workspace := Layer{Of: "the workspace", Settings: Settings{Rules: Rules{
		Allow: []string{"github__create_issue"},
		Deny:  []string{"github__delete_repo(main)"},
	}}}
	checker, err := New([]Layer{defaults, workspace}, []string{"touch", "github__list_issues"}, registry)
	if err != nil {
		t.Fatalf("rules for an unavailable provider's tools: %v", err)
	}
	if offered := checker.Tools(); len(offered) != 1 || offered[0].Name != "touch" {
		t.Errorf("offered %v, want only touch", offered)
	}
	for _, rule := range []string{"tuch", "githb__create_issue", "github", "files__touch"} {
		_, err := New([]Layer{defaults, layer(rule)}, nil, registry)
		if err == nil || !strings.HasPrefix(err.Error(), "the workspace: ") || !strings.Contains(err.Error(), "not in the tool registry") {
			t.Errorf("rule %q: %v, want it rejected in the workspace", rule, err)
		}
		if _, err := New([]Layer{defaults}, []string{rule}, registry); err == nil {
			t.Errorf("allowlist entry %q was accepted", rule)
		}
	}
}

func TestRuleForRemovedProvider(t *testing.T) {
	_, err := New([]Layer{defaults, layer("gitlab__merge")}, nil, testRegistry(t))
	if err == nil || !strings.HasPrefix(err.Error(), "the workspace: ") || !strings.Contains(err.Error(), `no tool provider is named "gitlab"`) {
		t.Errorf("got %v", err)
	}
}

func TestSaveRefusesNewRulesForUnavailableProviders(t *testing.T) {
	registry := testRegistry(t)
	base := []Layer{defaults}
	stored := Settings{Rules: Rules{Allow: []string{"github__create_issue"}}}

	kept := Layer{Of: "the workspace", Settings: Settings{ApprovalPolicy: string(Never), Rules: Rules{Deny: []string{" github__create_issue"}, Allow: []string{"touch"}}}}
	if err := ValidateLayer(base, stored, kept, registry); err != nil {
		t.Errorf("saving other changes beside a stored rule: %v", err)
	}
	for _, added := range []string{"github__creat_issue", "github__create_issue(x)"} {
		err := ValidateLayer(base, stored, layer("github__create_issue", added), registry)
		if err == nil || !strings.Contains(err.Error(), `the MCP server "github" is not connected`) || !strings.Contains(err.Error(), "the workspace: ") {
			t.Errorf("adding %q: %v", added, err)
		}
	}
	if err := ValidateLayer(base, Settings{}, layer("github__create_issue"), registry); err == nil {
		t.Error("a new layer was saved with a rule for an unavailable provider")
	}
}

func command(parts ...string) Call {
	line := strings.Join(parts, " && ")
	c := tools.Content{Text: line, Match: line}
	if len(parts) > 1 {
		c.Parts = parts
	}
	return Call{Tool: "exec", Content: c, HasContent: true}
}

func TestSuggestedRulesAllowTheCallNextTime(t *testing.T) {
	registry := testRegistry(t)
	checker, err := New([]Layer{defaults}, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call Call
		want []string
	}{
		{Call{Tool: "touch"}, []string{"touch"}},
		{command("git status --short"), []string{"exec(git status:*)"}},
		{command("rm -rf build"), []string{"exec(rm -rf build)"}},
		{command("pwd"), []string{"exec(pwd)"}},
		{Call{Tool: "click", Content: tools.Content{Text: "click Learn more — example.com", Match: "example.com"}, HasContent: true}, []string{"click(example.com)"}},
		{command("git add .", "go test ./...", "git add -A"), []string{"exec(git add:*)", "exec(go test:*)"}},
	} {
		d := checker.Decide(tc.call, nil)
		if d.Verdict != Ask || !slices.Equal(d.Suggested, tc.want) {
			t.Errorf("%q: verdict %v suggests %q, want ask with %q", tc.call.Content.Text, d.Verdict, d.Suggested, tc.want)
			continue
		}
		if a := d.Approval("a", tc.call, time.Time{}); !slices.Contains(a.AvailableDecisions(), repo.DecisionAcceptAlways) {
			t.Errorf("%q: offers %q", tc.call.Content.Text, a.AvailableDecisions())
		}
		allowed, err := New([]Layer{defaults, layer(d.Suggested...)}, nil, registry)
		if err != nil {
			t.Errorf("%q: %v", tc.call.Content.Text, err)
			continue
		}
		if v := allowed.Decide(tc.call, nil).Verdict; v != Run {
			t.Errorf("%q: verdict %v once %q are allowed", tc.call.Content.Text, v, d.Suggested)
		}
	}
}

func TestNoSuggestionWhereAnAllowRuleWouldNotHelp(t *testing.T) {
	registry := testRegistry(t)
	asking := Layer{Of: "the workspace", Settings: Settings{Rules: Rules{Ask: []string{"touch", "exec(curl:*)"}}}}
	checker, err := New([]Layer{defaults, asking}, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	opaque := command("sh -c $X")
	opaque.Content.Opaque = true
	escalated := command("make install")
	escalated.Prepared = &tools.Prepared{Sandboxed: true, Escalated: true, Justification: "writes /usr/local"}
	for what, call := range map[string]Call{
		"asked by a rule":     {Tool: "touch"},
		"not fully checkable": opaque,
		"outside the sandbox": escalated,
		"with a wildcard":     command("ls *.go"),
	} {
		d := checker.Decide(call, nil)
		if d.Verdict != Ask || d.Suggested != nil {
			t.Errorf("%s: verdict %v suggests %q", what, d.Verdict, d.Suggested)
		}
		if a := d.Approval("a", call, time.Time{}); slices.Contains(a.AvailableDecisions(), repo.DecisionAcceptAlways) {
			t.Errorf("%s: offers %q", what, a.AvailableDecisions())
		}
	}
}

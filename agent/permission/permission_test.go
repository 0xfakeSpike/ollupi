package permission

import (
	"context"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

type fileTools struct{}

func (fileTools) Name() (string, string) { return "files", "Touches files." }

func (fileTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{Name: "touch", InputSchema: map[string]any{"type": "object"}}}, nil
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

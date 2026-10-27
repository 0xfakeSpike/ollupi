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

// Rules and allowlist entries may name the tools of a provider that could
// not start, and are kept; every other unknown name is still a mistake.
func TestRulesForUnavailableProviders(t *testing.T) {
	registry, err := tools.New(context.Background(), []string{"github"}, fileTools{})
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{ApprovalPolicy: string(OnRequest), Rules: Rules{
		Allow: []string{"github__create_issue"},
		Deny:  []string{"github__delete_repo(main)"},
	}}
	checker, err := New(settings, []string{"touch", "github__list_issues"}, registry)
	if err != nil {
		t.Fatalf("rules for an unavailable provider's tools: %v", err)
	}
	if offered := checker.Tools(); len(offered) != 1 || offered[0].Name != "touch" {
		t.Errorf("offered %v, want only touch", offered)
	}
	for _, rule := range []string{"tuch", "githb__create_issue", "github", "files__touch"} {
		_, err := New(Settings{ApprovalPolicy: string(OnRequest), Rules: Rules{Allow: []string{rule}}}, nil, registry)
		if err == nil || !strings.Contains(err.Error(), "not in the tool registry") {
			t.Errorf("rule %q: %v, want it rejected", rule, err)
		}
		if _, err := New(Settings{ApprovalPolicy: string(OnRequest)}, []string{rule}, registry); err == nil {
			t.Errorf("allowlist entry %q was accepted", rule)
		}
	}
}

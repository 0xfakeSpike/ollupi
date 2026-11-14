package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestDeferredToolsAreListedByProvider(t *testing.T) {
	deferred := []tools.ToolDescriptor{{Provider: "github", Name: "github__issue"}, {Provider: "github", Name: "github__pr"}, {Provider: "slack", Name: "slack__post"}}
	text := system(t, fullMain, Input{Prompt: "AGENT", Root: "/p", Deferred: deferred})
	list := "# DEFERRED TOOLS\n"
	i, j := strings.Index(text, list), strings.Index(text, "# PROJECT")
	if i < 0 || j < i || !strings.Contains(text[i:j], "\n- github: github__issue, github__pr\n- slack: slack__post\n") {
		t.Errorf("prompt:\n%s", text)
	}
	if strings.Contains(system(t, fullMain, Input{Prompt: "AGENT"}), list) {
		t.Error("a prompt without deferred tools has a DEFERRED TOOLS section")
	}
}

// system is the role's prompt from in.
func system(t *testing.T, r Role, in Input) string {
	t.Helper()
	text, err := r.System(in)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// skillsFor installs a skill deploy for each of the roles, in a skills directory of their own.
func skillsFor(t *testing.T, roles ...Name) string {
	t.Helper()
	root := t.TempDir()
	for _, r := range roles {
		dir := filepath.Join(root, string(r), "deploy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Ship it.\n---\nShip it carefully.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestARoleListsTheSkillsItHasAndCompactIsShorter(t *testing.T) {
	root := skillsFor(t, FullMain, CompactMain, BrowserSub)
	for _, r := range all {
		listed := strings.Contains(system(t, r, Input{Prompt: "AGENT", SkillsDir: root, SkillTokens: 1000}), "# AVAILABLE SKILLS\n- deploy: Ship it.")
		if want := r.Name != GeneralSub; listed != want {
			t.Errorf("%s lists the skill: %v, want %v", r.Name, listed, want)
		}
	}
	compact := system(t, compactMain, Input{Prompt: "AGENT"})
	if !strings.HasPrefix(compact, "AGENT\n\n") || !strings.Contains(compact, "# Safety") || strings.Contains(compact, "# AVAILABLE SKILLS") {
		t.Errorf("compact:\n%s", compact)
	}
	if tokens := llm.EstimateTokens(compact); tokens > 400 {
		t.Errorf("the compact prompt takes %d tokens", tokens)
	}
}

func TestEachRoleWritesItsOwnPrompt(t *testing.T) {
	in := Input{Prompt: "AGENT", Root: "/p", SkillTokens: 1000}
	for _, r := range all {
		text := system(t, r, in)
		if !r.Persona && text != r.Rules {
			t.Errorf("%s has more than its own rules:\n%s", r.Name, text)
		}
		if r.Persona && (!strings.HasPrefix(text, "AGENT\n\n"+r.Rules) || !strings.HasSuffix(text, "/p; commands run there unless you set work_dir.")) {
			t.Errorf("%s does not put its rules between the agent's prompt and the conversation's sections:\n%s", r.Name, text)
		}
	}
	if general, _ := Of(GeneralSub); !strings.Contains(system(t, general, in), "# You are a subagent") {
		t.Error("the general subagent is not told its answer is a report")
	}
}

func TestTheAgentsPromptIsTheOperatorsOrNamesTheAgent(t *testing.T) {
	if text := system(t, fullMain, Input{Name: "Ollupi"}); !strings.HasPrefix(text, "You are Ollupi, a personal assistant") {
		t.Errorf("without the operator's prompt:\n%s", text)
	}
	if text := system(t, fullMain, Input{Name: "Ollupi", Prompt: "You are Helper."}); !strings.HasPrefix(text, "You are Helper.\n\n"+fullMain.Rules) {
		t.Errorf("with the operator's prompt:\n%s", text)
	}
}

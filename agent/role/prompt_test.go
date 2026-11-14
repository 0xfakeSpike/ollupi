package role

import (
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestDeferredToolsAreListedByProvider(t *testing.T) {
	deferred := []tools.ToolDescriptor{{Provider: "github", Name: "github__issue"}, {Provider: "github", Name: "github__pr"}, {Provider: "slack", Name: "slack__post"}}
	text := fullMain.System(Input{Agent: "AGENT", Root: "/p", Deferred: deferred})
	list := "# DEFERRED TOOLS\n"
	i, j := strings.Index(text, list), strings.Index(text, "# PROJECT")
	if i < 0 || j < i || !strings.Contains(text[i:j], "\n- github: github__issue, github__pr\n- slack: slack__post\n") {
		t.Errorf("prompt:\n%s", text)
	}
	if strings.Contains(fullMain.System(Input{Agent: "AGENT"}), list) {
		t.Error("a prompt without deferred tools has a DEFERRED TOOLS section")
	}
}

func TestCompactIsShorterAndListsNoSkills(t *testing.T) {
	catalog := []skills.Skill{{Name: "deploy", Description: "Ship it."}}
	full := fullMain.System(Input{Agent: "AGENT", Skills: catalog, SkillTokens: 1000})
	compact := compactMain.System(Input{Agent: "AGENT", Skills: catalog, SkillTokens: 1000})
	if !strings.Contains(full, "# AVAILABLE SKILLS") || strings.Contains(compact, "# AVAILABLE SKILLS") {
		t.Errorf("full:\n%s\ncompact:\n%s", full, compact)
	}
	if !strings.HasPrefix(compact, "AGENT\n\n") || !strings.Contains(compact, "# Safety") {
		t.Errorf("compact:\n%s", compact)
	}
	if tokens := llm.EstimateTokens(compact); tokens > 400 {
		t.Errorf("the compact prompt takes %d tokens", tokens)
	}
}

func TestEachRoleWritesItsOwnPrompt(t *testing.T) {
	in := Input{Agent: "AGENT", Root: "/p", Skills: []skills.Skill{{Name: "deploy", Description: "Ship it."}}, SkillTokens: 1000}
	for _, r := range all {
		text := r.System(in)
		if !r.Persona && text != r.Rules {
			t.Errorf("%s has more than its own rules:\n%s", r.Name, text)
		}
		if r.Persona && (!strings.HasPrefix(text, "AGENT\n\n"+r.Rules) || !strings.HasSuffix(text, "/p; commands run there unless you set work_dir.")) {
			t.Errorf("%s does not put its rules between the agent's prompt and the conversation's sections:\n%s", r.Name, text)
		}
	}
	if general, _ := Of(GeneralSub); !strings.Contains(general.System(in), "# You are a subagent") {
		t.Error("the general subagent is not told its answer is a report")
	}
}

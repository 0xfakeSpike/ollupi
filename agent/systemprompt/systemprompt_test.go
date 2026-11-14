package systemprompt

import (
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/profile"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

func TestDeferredToolsAreListedByProvider(t *testing.T) {
	deferred := []tools.ToolDescriptor{{Provider: "github", Name: "github__issue"}, {Provider: "github", Name: "github__pr"}, {Provider: "slack", Name: "slack__post"}}
	text := Build(Input{Agent: "AGENT", Profile: profile.Full, Root: "/p", Deferred: deferred})
	list := "# DEFERRED TOOLS\n"
	i, j := strings.Index(text, list), strings.Index(text, "# PROJECT")
	if i < 0 || j < i || !strings.Contains(text[i:j], "\n- github: github__issue, github__pr\n- slack: slack__post\n") {
		t.Errorf("prompt:\n%s", text)
	}
	if strings.Contains(Build(Input{Agent: "AGENT", Profile: profile.Full}), list) {
		t.Error("a prompt without deferred tools has a DEFERRED TOOLS section")
	}
}

func TestCompactIsShorterAndListsNoSkills(t *testing.T) {
	catalog := []skills.Skill{{Name: "deploy", Description: "Ship it."}}
	full := Build(Input{Agent: "AGENT", Profile: profile.Full, Skills: catalog, SkillTokens: 1000})
	compact := Build(Input{Agent: "AGENT", Profile: profile.Compact, Skills: catalog, SkillTokens: 1000})
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

func TestEachSubagentHasItsPrompt(t *testing.T) {
	in := Input{Agent: "AGENT", Profile: profile.Full, Root: "/p", Skills: []skills.Skill{{Name: "deploy", Description: "Ship it."}}, SkillTokens: 1000}
	for _, k := range subagent.Kinds {
		in.Subagent = &k
		text := Build(in)
		if k.Prompt != "" && text != k.Prompt {
			t.Errorf("%s has more than its own prompt:\n%s", k.Tool, text)
		}
		if k.Prompt == "" && (!strings.HasPrefix(text, "AGENT") || !strings.HasSuffix(text, subagent.Report)) {
			t.Errorf("%s does not run on the agent's prompt with the report rules:\n%s", k.Tool, text)
		}
	}
}

package role

import (
	"fmt"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/skills"
	"github.com/0xfakeSpike/ollupi/tools"
)

// projectSection names the project a run works in, by its root.
const projectSection = "# PROJECT\nThis conversation works in the project at %s; commands run there unless you set work_dir."

// defaultAgentPrompt is the agent's own prompt, which a role that answers the user starts with, when the operator
// gave none: it names the agent.
const defaultAgentPrompt = `You are %s, a personal assistant working on the user's own computer. You help one person get real things done: finding and organizing information, working with files, documents, and data, running tasks, and following up on them later.

# How you work
- Prefer doing to explaining: when a tool can do the task, do it and report what happened.
- Remember that the user may come back later: leave files, names, and notes that make the work easy to pick up again.

# Talking with the user
- Write plainly and warmly, as a capable colleague would. Lead with the outcome, then the details that matter to the user.`

// listSkills lists the skills installed for the role named role in skillsDir, where each role has its own, within
// budget tokens, saying how many more there are when they do not all fit: "" when there are none.
func listSkills(role Name, skillsDir string, budget int) (string, error) {
	if skillsDir == "" {
		return "", nil
	}
	catalog, err := skills.Load(skillsDir, string(role))
	if err != nil {
		return "", fmt.Errorf("role: the skills of %s: %w", role, err)
	}
	if len(catalog) == 0 {
		return "", nil
	}
	listed := catalog
	for len(listed) > 0 && llm.EstimateTokens(render(listed)) > budget {
		listed = listed[:len(listed)-1]
	}
	text := "# AVAILABLE SKILLS"
	if len(listed) > 0 {
		text += "\n" + render(listed)
	}
	if n := len(catalog) - len(listed); n > 0 {
		text += fmt.Sprintf("\n[%d more skills are not listed: the catalog is over its budget]", n)
	}
	return text, nil
}

func render(catalog []skills.Skill) string {
	lines := make([]string, len(catalog))
	for i, s := range catalog {
		lines[i] = "- " + s.Name + ": " + strings.Join(strings.Fields(s.Description), " ")
	}
	return strings.Join(lines, "\n")
}

func listDeferred(deferred []tools.ToolDescriptor) string {
	text := "# DEFERRED TOOLS\nThese tools are available, but their definitions are not loaded. Call " + loader.Tool +
		" with the exact names of the ones you need; a loaded tool stays callable for the rest of the conversation."
	var names []string
	for i, d := range deferred {
		names = append(names, d.Name)
		if i == len(deferred)-1 || deferred[i+1].Provider != d.Provider {
			text += "\n- " + d.Provider + ": " + strings.Join(names, ", ")
			names = nil
		}
	}
	return text
}

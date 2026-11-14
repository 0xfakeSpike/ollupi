package role

import (
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Input is what a run's system prompt is written from besides the role.
type Input struct {
	// Agent is the agent's own prompt, AgentPrompt.
	Agent       string
	Root        string
	Skills      []skills.Skill
	SkillTokens int
	Deferred    []tools.ToolDescriptor
}

// System writes the role's system prompt.
func (r Role) System(in Input) string {
	if !r.Persona {
		return r.Rules
	}
	parts := []string{strings.TrimSpace(in.Agent), r.Rules}
	if s := listSkills(in.Skills, in.SkillTokens); s != "" && r.Skills {
		parts = append(parts, s)
	}
	if len(in.Deferred) > 0 {
		parts = append(parts, listDeferred(in.Deferred))
	}
	if in.Root != "" {
		parts = append(parts, "# PROJECT\nThis conversation works in the project at "+in.Root+"; commands run there unless you set work_dir.")
	}
	return strings.Join(parts, "\n\n")
}

// AgentPrompt is the agent's own prompt: the operator's, or one that names the agent.
func AgentPrompt(name, prompt string) string {
	if strings.TrimSpace(prompt) != "" {
		return prompt
	}
	return fmt.Sprintf(defaultAgentPrompt, name)
}

const defaultAgentPrompt = `You are %s, a personal assistant working on the user's own computer. You help one person get real things done: finding and organizing information, working with files, documents, and data, running tasks, and following up on them later.

# How you work
- Prefer doing to explaining: when a tool can do the task, do it and report what happened.
- Remember that the user may come back later: leave files, names, and notes that make the work easy to pick up again.

# Talking with the user
- Write plainly and warmly, as a capable colleague would. Lead with the outcome, then the details that matter to the user.`

func listSkills(catalog []skills.Skill, budget int) string {
	if len(catalog) == 0 {
		return ""
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
	return text
}

func render(catalog []skills.Skill) string {
	lines := make([]string, len(catalog))
	for i, s := range catalog {
		lines[i] = "- " + s.Name + ": " + strings.Join(strings.Fields(s.Description), " ")
	}
	return strings.Join(lines, "\n")
}

func listDeferred(deferred []tools.ToolDescriptor) string {
	text := "# DEFERRED TOOLS\nThese tools are available, but their definitions are not loaded. Call " + tools.ToolLoadTools +
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

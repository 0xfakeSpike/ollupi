package prompt

import (
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

const instructionsSection = "# WORKSPACE INSTRUCTIONS\nThe user set these instructions for every conversation in this workspace. Follow them, within Safety.\n\n"

const projectSection = "# PROJECT\nThis conversation works in the project at %s; commands run there unless you set work_dir."

const defaultAgentPrompt = `You are %s, a personal assistant working on the user's own computer. You help one person get real things done: finding and organizing information, working with files, documents, and data, running tasks, and following up on them later.

# How you work
- Prefer doing to explaining: when a tool can do the task, do it and report what happened.
- Remember that the user may come back later: leave files, names, and notes that make the work easy to pick up again.

# Talking with the user
- Write plainly and warmly, as a capable colleague would. Lead with the outcome, then the details that matter to the user.`

type Skill struct {
	Name        string
	Description string
}

type System struct {
	Role         agent.Role
	Agent        string
	Prompt       string
	Instructions string
	Skills       []Skill
	Project      string
}

func (w Window) System(s System, deferred []Tool) string {
	var parts []string
	if s.Role.AnswersUser() {
		own := strings.TrimSpace(s.Prompt)
		if own == "" {
			own = fmt.Sprintf(defaultAgentPrompt, s.Agent)
		}
		parts = append(parts, own)
	}
	parts = append(parts, s.Role.Rules)
	if s.Instructions != "" {
		parts = append(parts, instructionsSection+s.Instructions)
	}
	if listed := w.listSkills(s.Skills); listed != "" {
		parts = append(parts, listed)
	}
	if len(deferred) > 0 {
		parts = append(parts, listDeferred(deferred))
	}
	if s.Project != "" {
		parts = append(parts, fmt.Sprintf(projectSection, s.Project))
	}
	return strings.Join(parts, "\n\n")
}

func (w Window) listSkills(catalog []Skill) string {
	if len(catalog) == 0 {
		return ""
	}
	listed := catalog
	for len(listed) > 0 && llm.EstimateTokens(renderSkills(listed)) > percentOf(w.inputTokens, skillPercent) {
		listed = listed[:len(listed)-1]
	}
	text := "# AVAILABLE SKILLS"
	if len(listed) > 0 {
		text += "\n" + renderSkills(listed)
	}
	if n := len(catalog) - len(listed); n > 0 {
		text += fmt.Sprintf("\n[%d more skills are not listed: the catalog is over its budget]", n)
	}
	return text
}

func renderSkills(catalog []Skill) string {
	lines := make([]string, len(catalog))
	for i, s := range catalog {
		lines[i] = "- " + s.Name + ": " + strings.Join(strings.Fields(s.Description), " ")
	}
	return strings.Join(lines, "\n")
}

func listDeferred(deferred []Tool) string {
	text := "# DEFERRED TOOLS\nThese tools are available, but their definitions are not loaded. Call " + agent.LoadToolsTool +
		" with the exact names of the ones you need; a loaded tool stays callable for the rest of the conversation."
	var names []string
	for i, t := range deferred {
		names = append(names, t.Name)
		if i == len(deferred)-1 || deferred[i+1].Provider != t.Provider {
			text += "\n- " + t.Provider + ": " + strings.Join(names, ", ")
			names = nil
		}
	}
	return text
}

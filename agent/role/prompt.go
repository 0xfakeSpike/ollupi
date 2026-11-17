package role

import (
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"github.com/0xfakeSpike/ollupi/tools/workspace"
)

const projectSection = "# PROJECT\nThis conversation works in the project at %s; commands run there unless you set work_dir."

const defaultAgentPrompt = `You are %s, a personal assistant working on the user's own computer. You help one person get real things done: finding and organizing information, working with files, documents, and data, running tasks, and following up on them later.

# How you work
- Prefer doing to explaining: when a tool can do the task, do it and report what happened.
- Remember that the user may come back later: leave files, names, and notes that make the work easy to pick up again.

# Talking with the user
- Write plainly and warmly, as a capable colleague would. Lead with the outcome, then the details that matter to the user.`

func (rt *Runtime) System() (string, error) {
	offered, deferred := rt.Offer()
	inProject := slices.ContainsFunc(slices.Concat(offered, deferred), func(d tools.ToolDescriptor) bool { return d.Provider == workspace.ProviderName })
	listed, err := rt.listSkills()
	if err != nil {
		return "", err
	}
	var parts []string
	if rt.role.answersUser {
		own := strings.TrimSpace(rt.prompt)
		if own == "" {
			own = fmt.Sprintf(defaultAgentPrompt, rt.agent)
		}
		parts = append(parts, own)
	}
	parts = append(parts, rt.role.rules)
	if listed != "" {
		parts = append(parts, listed)
	}
	if len(deferred) > 0 {
		parts = append(parts, listDeferred(deferred))
	}
	if inProject && rt.workspace.RootPath != "" {
		parts = append(parts, fmt.Sprintf(projectSection, rt.workspace.RootPath))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (rt *Runtime) listSkills() (string, error) {
	if rt.skillsDir == "" {
		return "", nil
	}
	catalog, err := skills.Load(rt.skillsDir, string(rt.role.name))
	if err != nil {
		return "", fmt.Errorf("role: the skills of %s: %w", rt.role.name, err)
	}
	budget := rt.window.SkillBudget()
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

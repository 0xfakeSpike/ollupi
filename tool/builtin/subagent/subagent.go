package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

type kind struct {
	title, description string
	reports            bool
}

var kinds = map[agent.Name]kind{
	agent.GeneralSub: {
		title: "Subagent",
		description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
			"For a task in the built-in browser, launch " + string(agent.BrowserSub) + " instead. " +
			"To make a picture (illustration, diagram, chart, icon, UI mockup), launch " + string(agent.DesignSub) + "; " +
			"to have a whole codebase read and explained, launch " + string(agent.CodebaseSub) + ". " +
			"To work on independent tasks at the same time, call " + string(agent.GeneralSub) + " several times in the same response; the subagents run in parallel.",
		reports: true,
	},
	agent.DelegateSub: {
		title: "Delegate",
		description: "Hand a task to a delegate: another run of you, with your tools, that works on it in a conversation of its own. " +
			"It never reports back: the user follows it and talks to it there. Use it for work this conversation does not need back; " +
			"for work whose result you need, launch " + string(agent.GeneralSub) + " instead. " +
			"The call returns at once and does not hold this turn. A delegate knows nothing of this conversation: put everything it needs in prompt.",
	},
	agent.BrowserSub: {
		title: "Browser subagent",
		description: "Launch a browser subagent: an agent that does one task in the built-in browser (opening pages, reading them, clicking and typing) and reports back. " +
			"It has only the browser's tools. Use it for browser work of many steps, so the pages it reads stay out of this conversation. " +
			"Browser subagents share the one browser, so launch one at a time.",
		reports: true,
	},
	agent.DesignSub: {
		title: "Design subagent",
		description: "Launch a design subagent to make a picture: an illustration, diagram, chart, icon, or UI mockup. " +
			"It draws from a source it leaves beside the result (SVG, HTML/CSS, or a plotting script), renders it, looks at the render and fixes it, then reports back " +
			"the paths, the size, and what it shows. Give it the content, the format or place it is for, and any style to follow; " +
			"use " + string(agent.GeneralSub) + " for work whose result is not a picture.",
		reports: true,
	},
	agent.CodebaseSub: {
		title: "Codebase subagent",
		description: "Launch a codebase subagent to read a whole project and explain it: what it is for, its modules and layers, " +
			"how a request or a piece of data flows through them, where state lives, its dependencies and constraints, and what a change would touch. " +
			"It reads the code itself, cites files and symbols, changes nothing, and reports back. " +
			"Use it for research in a project too big to read in this conversation.",
		reports: true,
	},
}

type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

type Provider struct{}

func (Provider) Name() (string, string) { return agent.SubagentsProvider, "Launches subagents." }

const reporting = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The call returns at once and the subagent works on its own; its report comes back to you as a message when it ends. " +
	"Meanwhile go on with what does not need the report, or end your reply: the turn waits for the reports of all its subagents, and answers the user if they write. "

const elsewhere = "It works in this conversation's workspace. Subagents cannot launch subagents."

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	specs := make([]kit.Spec, 0, len(kinds))
	for name, k := range kinds {
		description := k.description + " " + elsewhere
		if k.reports {
			description = k.description + " " + reporting + elsewhere
		}
		specs = append(specs, kit.Spec{
			Name:        string(name),
			Description: description,
			InputSchema: kit.InputSchema[Call](),
			Annotations: tool.Annotations{Title: k.title, ReadOnlyHint: true, OpenWorldHint: new(false)},
		})
	}
	return specs, nil
}

func (Provider) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	args, _ := kit.DecodeArguments[Call](c.Arguments)
	if strings.TrimSpace(args.Prompt) == "" || strings.TrimSpace(args.Description) == "" {
		return kit.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	return kit.Result{Launch: &tool.Launch{Role: c.Tool, Title: strings.TrimSpace(args.Description), Task: args.Prompt}}, nil
}

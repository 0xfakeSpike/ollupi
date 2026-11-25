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

// kind is what the tool menu says about a role. Whether it reports back comes from agent.Reporting,
// so the role table stays the one place that decides it.
type kind struct {
	title, description string
}

var kinds = map[agent.Name]kind{
	agent.GeneralSub: {
		title: "Subagent",
		description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
			"When the task is one of these, launch the specialist instead: " + string(agent.BrowserSub) + " for work in the built-in browser; " +
			string(agent.DesignSub) + " for a picture, an icon, a poster or a UI mockup; " + string(agent.CodebaseSub) + " to find out how a project's own code works; " +
			string(agent.CodingSub) + " to build or change a project's code, front end and back end alike, with its tests; " +
			string(agent.WritingSub) + " to write or rewrite a text; " + string(agent.ResearchSub) + " to find out what is true outside the project, from the web and documents; " +
			string(agent.VerifySub) + " to check finished work against what it must do. " +
			"To work on independent tasks at the same time, call " + string(agent.GeneralSub) + " several times in the same response; the subagents run in parallel.",
	},
	agent.BrowserSub: {
		title: "Browser subagent",
		description: "Launch a browser subagent: an agent that does one task in the built-in browser (opening pages, reading them, clicking and typing) and reports back. " +
			"It has only the browser's tools. Use it for browser work of many steps, so the pages it reads stay out of this conversation. " +
			"Browser subagents share the one browser, so launch one at a time.",
	},
	agent.DesignSub: {
		title: "Design subagent",
		description: "Launch a design subagent to make a picture: an illustration, diagram, chart, icon, logo, poster, banner, cover image, or UI mockup. " +
			"It draws from a source it leaves beside the result (SVG, HTML/CSS, or a plotting script), renders it, looks at the render and fixes it, then reports back " +
			"the paths, the size, and what it shows. Give it the content, the format or place it is for, and any style to follow; " +
			"use " + string(agent.GeneralSub) + " for work whose result is not a picture.",
	},
	agent.CodebaseSub: {
		title: "Codebase subagent",
		description: "Launch a codebase subagent to read a whole project and explain it: what it is for, its modules and layers, " +
			"how a request or a piece of data flows through them, where state lives, its dependencies and constraints, and what a change would touch. " +
			"It reads the code itself, cites files and symbols, cannot edit or write files, and reports back; a document you ask for comes back in its report for you to write. " +
			"Use it for any question the project's own code settles, in a project too big to read in this conversation; " +
			"use " + string(agent.ResearchSub) + " for what lies outside the project.",
	},
	agent.CodingSub: {
		title: "Coding subagent",
		description: "Launch a coding subagent to build or change a project's code — its interface, its servers and command-line tools, its data and migrations — together with the tests around the change. " +
			"It reads the project's conventions first and keeps them, changes every side a feature crosses in one pass, proves the change with the project's build and tests and, for an interface, by looking at it in the browser, " +
			"and reports back the files it touched, what it ran and what that returned. Give it the project, the behaviour or screen wanted, and any constraint that must hold.",
	},
	agent.WritingSub: {
		title: "Writing subagent",
		description: "Launch a writing subagent to write or rewrite a text for a named reader and place: a document, README, article, post, email or report. " +
			"It keeps the facts, names and voice it finds in the project and in the material you point it at, writes the text into the file the task names, and reports back the path, what it changed and what it could not settle. " +
			"Give it the reader, the purpose, the source material and the language.",
	},
	agent.VerifySub: {
		title: "Verify subagent",
		description: "Launch a verify subagent to check finished work against what it must do: it builds the project, runs its whole test suite and checks, " +
			"tries the behaviour itself — a command, a request, a page in the browser — including the cases that should fail, and reports back a verdict for each criterion " +
			"with the command and output that show it, and what it could not check. It cannot edit files and fixes nothing. " +
			"Give it what was changed and where, and the criteria the work must meet; it takes your account of the work as the claim to test, not as evidence. " +
			"Use it after a change of several files or steps, before you tell the user the work is done.",
	},
	agent.ResearchSub: {
		title: "Research subagent",
		description: "Launch a research subagent to gather information from outside the project and report what is true: the public web, a site, or documents you name or put in the workspace. " +
			"It reads only and cannot edit or write files, cites each finding with its URL or path, separates what a source says from what it inferred, and reports back what it could not confirm; " +
			"a document you ask for comes back in its report for you to write. " +
			"Give it the question, how deep to go, and what would count as an answer; " +
			"use " + string(agent.CodebaseSub) + " for how the project's own code works.",
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
	"Meanwhile go on only with work the subagent does not cover, never its task, which it is already doing, and finish what depends on its report only after the report comes back; or end your reply: the turn waits for the reports of all its subagents, and answers the user if they write. "

const elsewhere = "It works in this conversation's workspace. Subagents cannot launch subagents."

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	specs := make([]kit.Spec, 0, len(kinds))
	for name, k := range kinds {
		description := k.description + " " + elsewhere
		if agent.Reporting(string(name)) {
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

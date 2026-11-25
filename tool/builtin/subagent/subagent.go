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
		description: "Launch a general-purpose subagent: another run of you, with your tools and skills, that does one task on its own and reports back — " +
			"writing code, a text or a picture, working in the built-in browser, or anything else that needs acting. " +
			"When the task only needs reading, launch " + string(agent.ExploreSub) + " instead; to check finished work, launch " + string(agent.VerifySub) + ". " +
			"To work on independent tasks at the same time, call " + string(agent.GeneralSub) + " several times in the same response; the subagents run in parallel, " +
			"so never give two of them work on the same files, and give work in the built-in browser to one at a time, since there is one browser.",
	},
	agent.VerifySub: {
		title: "Verify subagent",
		description: "Launch a verify subagent to check finished work against what it must do: it builds the project, runs its whole test suite and checks, " +
			"tries the behaviour itself — a command, a request, a page in the browser — including the cases that should fail, and reports back a verdict for each criterion " +
			"with the command and output that show it, and what it could not check. It cannot edit files and fixes nothing. " +
			"Give it what was changed and where, and the criteria the work must meet; it takes your account of the work as the claim to test, not as evidence. " +
			"Use it after a change of several files or steps, before you tell the user the work is done.",
	},
	agent.ExploreSub: {
		title: "Explore subagent",
		description: "Launch an explore subagent to find out what is true: how a project's code works — its modules and layers, how a request or a piece of data flows through them, where state lives, what a change would touch — " +
			"or what the web, a site or a document says. It cannot change anything: it reads, runs only commands that read, cites each finding with its path or URL, separates what a source says from what it inferred, " +
			"and reports back what it could not confirm; a document you ask for comes back in its report for you to write. Several can run at once on independent questions. " +
			"Give it the question, how deep to go, and what would count as an answer.",
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

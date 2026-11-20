package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

type kind struct {
	title, description string
}

var kinds = map[agent.Name]kind{
	agent.GeneralSub: {
		title: "Subagent",
		description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
			"For a task in the built-in browser, launch " + string(agent.BrowserSub) + " instead. " +
			"To work on independent tasks at the same time, call " + string(agent.GeneralSub) + " several times in the same response; the subagents run in parallel.",
	},
	agent.BrowserSub: {
		title: "Browser subagent",
		description: "Launch a browser subagent: an agent that does one task in the built-in browser (opening pages, reading them, clicking and typing) and reports back. " +
			"It has only the browser's tools. Use it for browser work of many steps, so the pages it reads stay out of this conversation. " +
			"Browser subagents share the one browser, so launch one at a time.",
	},
}

type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
	Workspace   string `json:"workspace,omitempty" jsonschema:"Id of the workspace the subagent works in; leave it out for this conversation's workspace."`
	Wait        *bool  `json:"wait,omitempty" jsonschema:"Whether this turn waits for the subagent's report. With false the subagent works on its own and never reports back."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
	s.Properties["workspace"].MinLength = jsonschema.Ptr(1)
	s.Properties["wait"].Type, s.Properties["wait"].Types, s.Properties["wait"].Default = "boolean", nil, json.RawMessage("true")
}

type Provider struct{ st store.Store }

func New(st store.Store) Provider { return Provider{st: st} }

func (Provider) Name() (string, string) { return agent.SubagentsProvider, "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The call returns at once and the subagent works on its own; by default its report comes back to you as a message when it ends. " +
	"Meanwhile go on with what does not need the report, or end your reply: the turn waits for the reports of the subagents it waits for, and answers the user if they write. " +
	"Pass wait false for work this turn does not need back: that subagent never reports, and the user follows it in its own conversation. " +
	"To work in another workspace, pass its id as workspace; " + agent.InfoTool + " lists the workspaces. " +
	"Subagents cannot launch subagents."

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	specs := make([]kit.Spec, 0, len(kinds))
	for name, k := range kinds {
		specs = append(specs, kit.Spec{
			Name:        string(name),
			Description: k.description + " " + launching,
			InputSchema: kit.InputSchema[Call](),
			Annotations: tool.Annotations{Title: k.title, ReadOnlyHint: true, OpenWorldHint: new(false)},
		})
	}
	return specs, nil
}

func (p Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, _ := kit.DecodeArguments[Call](c.Arguments)
	if strings.TrimSpace(args.Prompt) == "" || strings.TrimSpace(args.Description) == "" {
		return kit.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	workspace := c.Workspace
	if args.Workspace != "" {
		if err := p.usable(ctx, c.User, args.Workspace); err != nil {
			return kit.Result{}, err
		}
		workspace = args.Workspace
	}
	detached := args.Wait != nil && !*args.Wait
	return kit.Result{Launch: &tool.Launch{Role: c.Tool, Title: strings.TrimSpace(args.Description), Task: args.Prompt,
		Workspace: workspace, Detached: detached}}, nil
}

func (p Provider) usable(ctx context.Context, user, id string) error {
	var w account.Workspace
	err := p.st.Read(ctx, func(r store.Reader) (err error) { w, err = r.Workspace(user, id); return err })
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("the user has no workspace %q; %s lists the workspaces", id, agent.InfoTool)
	}
	if err != nil {
		return err
	}
	return w.Usable()
}

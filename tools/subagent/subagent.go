// Package subagent gives an agent the tools that launch subagents. A call does not run the subagent: it writes the
// subagent's conversation and its first turn, ready, which the orchestrator runs as it runs any, and completes at
// once, as a call in the background does. The subagent's report comes back to the turn that launched it as an item
// of its own when the subagent ends, and that turn does not end before its subagents have.
package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// ProviderName is the provider of the tools that launch subagents.
const ProviderName = "subagents"

const (
	// General and Browser are the kinds of subagent. Each is the name of the tool that launches it, and of the role
	// that its subagent runs as.
	General = "general-sub-agent"
	Browser = "browser-sub-agent"
)

// A Kind is how the tool that launches a kind of subagent shows: its title to the user, and its description to the
// launching agent, which tells it when to launch the kind; the tool adds how launching works.
type Kind struct {
	Title, Description string
}

// Kinds are the kinds of subagent, by name.
var Kinds = map[string]Kind{
	General: {
		Title: "Subagent",
		Description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
			"For a task in the built-in browser, launch " + Browser + " instead. " +
			"To work on independent tasks at the same time, call " + General + " several times in the same response; the subagents run in parallel.",
	},
	Browser: {
		Title: "Browser subagent",
		Description: "Launch a browser subagent: an agent that does one task in the built-in browser (opening pages, reading them, clicking and typing) and reports back. " +
			"It has only the browser's tools. Use it for browser work of many steps, so the pages it reads stay out of this conversation. " +
			"Browser subagents share the one browser, so launch one at a time.",
	},
}

// A Call is what a launch tool is called with: the task for the subagent.
type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

// Launch is what a launch returns at once: the subagent's conversation, which a client opens, and its task.
type Launch struct {
	Subagent    string `json:"subagent"`
	Description string `json:"description"`
	Note        string `json:"note"`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

// Tools provides a tool for each kind of subagent.
type Tools struct {
	db *db.DB
	// started tells the orchestrator of a subagent written, whose turn waits for a slot.
	started func(threadID string)
}

func New(store *db.DB, started func(threadID string)) *Tools {
	return &Tools{db: store, started: started}
}

func (*Tools) Name() (string, string) { return ProviderName, "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The call returns at once and the subagent works on its own; its report comes back to you as a message when it ends. " +
	"Meanwhile go on with what does not need the report, or end your reply: the turn waits for the reports of all its subagents, and answers the user if they write. " +
	"Subagents cannot launch subagents."

func (t *Tools) ToolList(context.Context) ([]*tools.Spec, error) {
	specs := make([]*tools.Spec, 0, len(Kinds))
	for name, k := range Kinds {
		specs = append(specs, &tools.Spec{
			Name:        name,
			Description: k.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: k.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

// Invoke writes the subagent of the kind whose tool was called and returns at once: its report comes back later.
func (t *Tools) Invoke(ctx context.Context, caller tools.Caller, name string, arguments map[string]any) (tools.Result, error) {
	c, _ := tools.DecodeArguments[Call](arguments) // the schema checked its fields: it cannot fail
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	child, err := t.db.WriteSubagent(caller.ThreadID, caller.Item, c.Description, db.TurnRow{Agent: name}, c.Prompt)
	if err != nil {
		return tools.Result{}, err
	}
	t.started(child.ID)
	return tools.Result{Output: Launch{
		Subagent: child.ID, Description: child.Title,
		Note: "It works on its own; its report comes back to this conversation when it ends.",
	}}, nil
}

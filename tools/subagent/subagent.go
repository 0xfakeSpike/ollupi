// Package subagent gives an agent the tools that launch subagents. A call does not run the subagent: it writes the
// subagent's conversation and its first turn, queued, and waits on that conversation, which the orchestrator starts
// and runs as it runs any, and whose final answer comes back as the call's result.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// ProviderName is the provider of the tools that launch subagents.
const ProviderName = "subagents"

// A Kind is a kind of subagent: the tool that launches it, as the launching agent sees it, and the role it runs as.
type Kind struct {
	Tool, Title string
	// Description tells the launching agent when to launch the kind; the tool adds how launching works.
	Description string
	Role        string
}

// A Call is what a launch tool is called with: the task for the subagent.
type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

// Tools provides a tool for each kind of subagent.
type Tools struct {
	db    *repo.DB
	kinds []Kind
}

func New(db *repo.DB, kinds []Kind) *Tools { return &Tools{db: db, kinds: kinds} }

func (*Tools) Name() (string, string) { return ProviderName, "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The turn waits until every subagent launched in the response has reported, and each call returns that subagent's final report. Subagents cannot launch subagents."

func (t *Tools) ToolList(context.Context) ([]*tools.Spec, error) {
	specs := make([]*tools.Spec, 0, len(t.kinds))
	for _, k := range t.kinds {
		specs = append(specs, &tools.Spec{
			Name:        k.Tool,
			Description: k.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: k.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

// Invoke queues the subagent of the kind whose tool was called, and waits on its conversation.
func (t *Tools) Invoke(ctx context.Context, caller tools.Caller, name string, arguments map[string]any) (tools.Result, error) {
	i := slices.IndexFunc(t.kinds, func(k Kind) bool { return k.Tool == name })
	if i < 0 {
		return tools.Result{}, fmt.Errorf("%q launches no subagent", name)
	}
	c, err := tools.DecodeArguments[Call](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	item := caller.Item
	if item == "" {
		return tools.Result{}, fmt.Errorf("a subagent is launched only by a call of a turn")
	}
	child, err := t.db.QueueSubagent(caller.ThreadID, item, c.Description, repo.TurnRow{Input: c.Prompt, Agent: t.kinds[i].Role})
	if err != nil {
		return tools.Result{}, err
	}
	request, err := json.Marshal(c)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Await: &tools.Await{By: tools.AwaitAgent, Request: request, Handle: child.ID}}, nil
}

package role

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

func Decode(raw json.RawMessage) (Call, error) {
	var c Call
	if err := json.Unmarshal(raw, &c); err != nil {
		return Call{}, fmt.Errorf("agent call: %w", err)
	}
	return c, nil
}

// Launchers provides the tools that launch the subagent roles.
type Launchers struct{}

func (Launchers) Name() (string, string) { return "subagents", "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The turn waits until every subagent launched in the response has reported, and each call returns that subagent's final report. Subagents cannot launch subagents."

func (Launchers) ToolList(context.Context) ([]*tools.Spec, error) {
	var specs []*tools.Spec
	for _, r := range all {
		if r.Launcher == nil {
			continue
		}
		specs = append(specs, &tools.Spec{
			Name:        r.Launcher.Tool,
			Description: r.Launcher.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: r.Launcher.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

func (Launchers) Invoke(_ context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	c, err := tools.DecodeArguments[Call](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	return tools.Awaiting(tools.AwaitAgent, c)
}

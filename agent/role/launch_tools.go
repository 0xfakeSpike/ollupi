package role

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// A Call is what a launch tool is called with: the task for the subagent.
type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

// A Launch is what a launch tool's call waits on: the role it launches, with the task.
type Launch struct {
	Role Name `json:"role"`
	Call
}

func Decode(raw json.RawMessage) (Launch, error) {
	var l Launch
	if err := json.Unmarshal(raw, &l); err != nil {
		return Launch{}, fmt.Errorf("launch: %w", err)
	}
	return l, nil
}

// LaunchTools provides the tools that launch the subagent roles.
type LaunchTools struct{}

// LaunchToolsProvider is the provider of the launch tools.
const LaunchToolsProvider = "subagents"

func (LaunchTools) Name() (string, string) { return LaunchToolsProvider, "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The turn waits until every subagent launched in the response has reported, and each call returns that subagent's final report. Subagents cannot launch subagents."

func (LaunchTools) ToolList(context.Context) ([]*tools.Spec, error) {
	var specs []*tools.Spec
	for _, r := range all {
		if r.LaunchedBy == nil {
			continue
		}
		specs = append(specs, &tools.Spec{
			Name:        r.LaunchedBy.Name,
			Description: r.LaunchedBy.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: r.LaunchedBy.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

// Invoke waits on the launch of the role whose tool was called. The role is named in what the call waits on, so that
// whoever launches the subagent goes by the role, never by the tool.
func (LaunchTools) Invoke(_ context.Context, name string, arguments map[string]any) (tools.Result, error) {
	i := slices.IndexFunc(all, func(r Role) bool { return r.LaunchedBy != nil && r.LaunchedBy.Name == name })
	if i < 0 {
		return tools.Result{}, fmt.Errorf("%q launches no role", name)
	}
	c, err := tools.DecodeArguments[Call](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	return tools.Awaiting(tools.AwaitAgent, Launch{Role: all[i].Name, Call: c})
}

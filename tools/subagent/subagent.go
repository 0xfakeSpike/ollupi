package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/google/jsonschema-go/jsonschema"
)

// A Kind is one kind of subagent: the tool that launches it, and how the subagent runs.
type Kind struct {
	Tool, Title string
	// Description tells the launching agent when to use the kind; ToolList adds how launching works.
	Description string
	// Prompt is the kind's whole system prompt. Without one, the subagent runs on the agent's own prompt with Report
	// after it.
	Prompt string
	// Provider, when set, is the only provider whose tools the subagent is offered, and the kind is offered only
	// while that provider has a tool.
	Provider string
	// Memory loads the user's memory for the subagent, as for a top-level thread.
	Memory bool
}

// Kinds are every kind of subagent, in the order their tools are listed. Each kind is defined in its own file.
var Kinds = []Kind{general, browser}

func KindOf(tool string) (Kind, bool) {
	i := slices.IndexFunc(Kinds, func(k Kind) bool { return k.Tool == tool })
	if i < 0 {
		return Kind{}, false
	}
	return Kinds[i], true
}

// Offers says whether a subagent of the kind is offered d. No subagent can launch another or schedule tasks, which
// would come back to its thread after it reported.
func (k Kind) Offers(d tools.ToolDescriptor) bool {
	if _, launches := KindOf(d.Name); launches || d.Provider == cron.ProviderName {
		return false
	}
	return k.Provider == "" || d.Provider == k.Provider
}

// Report follows the agent's own prompt for a kind without a prompt of its own.
const Report = `# You are a subagent
Another agent launched you to do one task. Your final answer is your report to that agent, not a message to the user: make it complete on its own, with what the task asked for.`

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

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Name() (string, string) { return "subagents", "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The turn waits until every subagent launched in the response has reported, and each call returns that subagent's final report. Subagents cannot launch subagents."

func (p *Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	specs := make([]*tools.Spec, 0, len(Kinds))
	for _, k := range Kinds {
		specs = append(specs, &tools.Spec{
			Name:        k.Tool,
			Description: k.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: k.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

func (p *Provider) Invoke(_ context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	c, err := tools.DecodeArguments[Call](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	return tools.Awaiting(tools.AwaitAgent, c)
}

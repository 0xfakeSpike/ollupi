package plan

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const Tool = "update_plan"

type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
)

var StatusValues = []Status{Pending, InProgress, Completed}

const maxSteps = 12

type Step struct {
	Step   string `json:"step"`
	Status Status `json:"status"`
}

type Plan struct {
	Explanation string `json:"explanation,omitempty"`
	Steps       []Step `json:"plan"`
}

func (p Plan) Validate() error {
	if len(p.Steps) == 0 || len(p.Steps) > maxSteps {
		return fmt.Errorf("plan requires 1 to %d steps", maxSteps)
	}
	inProgress := 0
	for i, step := range p.Steps {
		if strings.TrimSpace(step.Step) == "" {
			return fmt.Errorf("plan step %d is empty", i+1)
		}
		if !slices.Contains(StatusValues, step.Status) {
			return fmt.Errorf("plan step %d has invalid status %q", i+1, step.Status)
		}
		if step.Status == InProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("at most one plan step can be in_progress")
	}
	return nil
}

func (Status) Constrain(s *jsonschema.Schema) {
	for _, v := range StatusValues {
		s.Enum = append(s.Enum, v)
	}
}

func (Step) Constrain(s *jsonschema.Schema) {
	s.Properties["step"].MinLength, s.Properties["step"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(200)
}

func (Plan) Constrain(s *jsonschema.Schema) {
	s.Properties["explanation"].MaxLength = jsonschema.Ptr(500)
	s.Properties["plan"].MinItems, s.Properties["plan"].MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(maxSteps)
}

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Name() (string, string) { return "plan", "Keeps the task plan the user sees." }

func (*Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: Tool,
		Description: "Updates the task plan the user sees. For non-trivial work with several distinct steps, call it with a short plan and keep it current: " +
			"exactly one step in_progress while you work on it, and mark steps completed as soon as they are done. Skip it for simple requests. Each call replaces the whole plan.",
		InputSchema: tools.InputSchema[Plan](),
		Annotations: &tools.Annotations{Title: "Plan", ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (*Provider) Invoke(_ context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	p, err := tools.DecodeArguments[Plan](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if err := p.Validate(); err != nil {
		return tools.Result{}, fmt.Errorf("invalid plan: %w", err)
	}
	return tools.Result{Output: "Plan updated"}, nil
}

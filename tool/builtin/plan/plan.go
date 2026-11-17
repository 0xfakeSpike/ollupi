package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

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

func (p Plan) validate() error {
	inProgress := 0
	for i, step := range p.Steps {
		if strings.TrimSpace(step.Step) == "" {
			return fmt.Errorf("plan step %d is empty", i+1)
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

func (Provider) Name() (string, string) {
	return agent.PlanProvider, "Keeps the task plan the user sees."
}

func (Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: agent.PlanTool,
		Description: "Updates the task plan the user sees. For non-trivial work with several distinct steps, call it with a short plan and keep it current: " +
			"exactly one step in_progress while you work on it, and mark steps completed as soon as they are done. Skip it for simple requests. Each call replaces the whole plan.",
		InputSchema: kit.InputSchema[Plan](),
		Annotations: tool.Annotations{Title: "Plan", ReadOnlyHint: true, OpenWorldHint: kit.Hint(false)},
	}}, nil
}

func (Provider) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	p, _ := kit.DecodeArguments[Plan](c.Arguments)
	if err := p.validate(); err != nil {
		return kit.Result{}, fmt.Errorf("invalid plan: %w", err)
	}
	return kit.Result{Output: "Plan updated"}, nil
}

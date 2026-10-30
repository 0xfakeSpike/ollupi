package userinput

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const Tool = "request_user_input"

type Question struct {
	ID       string   `json:"id" jsonschema:"Stable identifier for mapping answers (snake_case)."`
	Header   string   `json:"header" jsonschema:"Short header label shown in the UI (12 or fewer chars)."`
	Question string   `json:"question" jsonschema:"Single-sentence prompt shown to the user, in the user's language."`
	Options  []Option `json:"options,omitempty" jsonschema:"Optional mutually exclusive choices. The user can always answer in their own words instead."`
}

func (Question) Constrain(s *jsonschema.Schema) {
	s.Properties["id"].Pattern = "^[a-z][a-z0-9_]{0,63}$"
	s.Properties["header"].MinLength, s.Properties["header"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(12)
	s.Properties["question"].MinLength, s.Properties["question"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(600)
	s.Properties["options"].MaxItems = jsonschema.Ptr(4)
}

type Option struct {
	Label       string `json:"label" jsonschema:"User-facing label (1-5 words)."`
	Description string `json:"description,omitempty" jsonschema:"One short sentence explaining the impact of this option."`
}

func (Option) Constrain(s *jsonschema.Schema) {
	s.Properties["label"].MinLength, s.Properties["label"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(60)
	s.Properties["description"].MaxLength = jsonschema.Ptr(200)
}

type Answer struct {
	Answers []string `json:"answers"`
}

type Answers map[string]Answer

type Request struct {
	Questions []Question `json:"questions" jsonschema:"Questions to show the user. Prefer 1 and do not exceed 3."`
}

func (Request) Constrain(s *jsonschema.Schema) {
	s.Properties["questions"].MinItems, s.Properties["questions"].MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(3)
}

func Decode(raw json.RawMessage) (Request, error) {
	var r Request
	if err := json.Unmarshal(raw, &r); err != nil {
		return Request{}, fmt.Errorf("user input request: %w", err)
	}
	return r, nil
}

func (r Request) validate() error {
	seen := make(map[string]bool, len(r.Questions))
	for _, q := range r.Questions {
		if seen[q.ID] {
			return fmt.Errorf("question id %q is duplicated", q.ID)
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Question) == "" || strings.TrimSpace(q.Header) == "" {
			return fmt.Errorf("question %q needs a non-blank header and question", q.ID)
		}
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return fmt.Errorf("question %q has a blank option label", q.ID)
			}
		}
	}
	return nil
}

func (r Request) Check(answers Answers, maxRunes int) error {
	total := 0
	for _, q := range r.Questions {
		answer, ok := answers[q.ID]
		if !ok || len(answer.Answers) == 0 {
			return fmt.Errorf("question %q has no answer", q.ID)
		}
		for _, text := range answer.Answers {
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("question %q has a blank answer", q.ID)
			}
			total += len([]rune(text))
		}
	}
	if len(answers) != len(r.Questions) {
		return fmt.Errorf("answers name a question that was not asked")
	}
	if total > maxRunes {
		return fmt.Errorf("answers have %d characters; at most %d are accepted", total, maxRunes)
	}
	return nil
}

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Name() (string, string) { return "user_input", "Asks the user questions." }

func (*Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: Tool,
		Description: "Asks the user one to three short questions and waits for the answers. " +
			"Make it the only call of its response: other calls in the same response run before the user answers.",
		InputSchema: tools.InputSchema[Request](),
		Annotations: &tools.Annotations{Title: "Question", ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (*Provider) Invoke(_ context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	r, err := tools.DecodeArguments[Request](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if err := r.validate(); err != nil {
		return tools.Result{}, fmt.Errorf("invalid questions: %w", err)
	}
	return tools.Awaiting(tools.AwaitUser, r)
}

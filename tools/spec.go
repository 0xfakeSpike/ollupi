package tools

import (
	"context"
	"fmt"
	"maps"
)

type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

type Spec struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema any          `json:"inputSchema"`
	Annotations *Annotations `json:"annotations,omitempty"`

	PermissionContent func(ctx context.Context, arguments map[string]any) (Content, error) `json:"-"`
	Local             *Local                                                               `json:"-"`
	Serial            bool                                                                 `json:"-"`
	// Background says a call may run in the background, as its ArgBackground argument asks.
	Background bool `json:"-"`
}

// ArgBackground is the argument a call of a Background tool asks to run in the background with. The runtime adds it to
// the tool's schema and takes it out of the arguments before the tool sees them.
const ArgBackground = "run_in_background"

type Content struct {
	Text   string
	Match  string
	Parts  []string
	Opaque bool
}

type Local struct {
	Prepare func(ctx context.Context, arguments map[string]any) (*Prepared, error)
	Run     func(ctx context.Context, arguments map[string]any, p *Prepared) (Result, error)
}

type ToolDescriptor struct {
	Provider    string         `json:"provider"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Annotations Annotations    `json:"annotations"`
	InputSchema map[string]any `json:"input_schema"`
	Serial      bool           `json:"-"`
	Background  bool           `json:"background,omitempty"`
}

func OpenWorld(a Annotations) bool {
	return a.OpenWorldHint == nil || *a.OpenWorldHint
}

func Hint(v bool) *bool {
	return &v
}

// ModelSchema is the input schema the model is sent for the tool: a Background tool takes ArgBackground besides its
// own arguments.
func (d ToolDescriptor) ModelSchema() map[string]any {
	if !d.Background {
		return d.InputSchema
	}
	properties := map[string]any{}
	if own, ok := d.InputSchema["properties"].(map[string]any); ok {
		properties = maps.Clone(own)
	}
	properties[ArgBackground] = map[string]any{
		"type": "boolean",
		"description": "Run the call in the background: it returns at once with a background call id, and its result comes back " +
			"to the conversation when it ends. Leave it out for a quick call, or one whose result you need before going on.",
	}
	out := maps.Clone(d.InputSchema)
	out["properties"] = properties
	return out
}

// TakeBackground takes ArgBackground out of the arguments of a call of a Background tool, as the tool never sees it,
// and reports whether the call asked to run in the background. Any other tool keeps an argument of that name as its
// own.
func (d ToolDescriptor) TakeBackground(args map[string]any) (bool, error) {
	if !d.Background {
		return false, nil
	}
	v, given := args[ArgBackground]
	delete(args, ArgBackground)
	if !given {
		return false, nil
	}
	background, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be true or false", ArgBackground)
	}
	return background, nil
}

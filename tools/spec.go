package tools

import (
	"context"
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

package tool

import (
	"context"
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
)

type Tools func(ctx context.Context, user string) (Set, error)

type Set interface {
	Specs() []Spec
	Unavailable() []string
	Content(ctx context.Context, c Call) (policy.Content, error)
	Prepare(ctx context.Context, c Call) (Plan, error)
	Invoke(ctx context.Context, c Call) Result
}

func Toolbox(s Set) policy.Toolbox {
	box := policy.Toolbox{Unavailable: s.Unavailable()}
	for _, t := range s.Specs() {
		box.Tools = append(box.Tools, policy.Tool{Name: t.Name, Provider: t.Provider, Content: t.Content})
	}
	return box
}

type Spec struct {
	Name        string
	Provider    string
	Description string
	Schema      map[string]any
	Annotations Annotations
	Content     bool
	Serial      bool
	Background  Background
}

type Background string

const (
	BackgroundNever    Background = ""
	BackgroundOptional Background = "optional"
	BackgroundAlways   Background = "always"
)

const BackgroundArgument = "background"

func (s Spec) InBackground(args map[string]any) bool {
	return s.Background == BackgroundAlways || s.Background == BackgroundOptional && args[BackgroundArgument] == true
}

type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

func (a Annotations) OpenWorld() bool { return a.OpenWorldHint == nil || *a.OpenWorldHint }

type Caller struct {
	User        string
	Thread      string
	Workspace   string
	Environment string
	Root        string
	Subagent    bool
	Role        string
	Item        string
	Deferred    []string
}

type Call struct {
	Caller
	Tool      string
	Arguments map[string]any
	Prepared  json.RawMessage
}

type Plan struct {
	Data      json.RawMessage
	Sandboxed bool
	Escalated bool
}

type Result struct {
	Output    json.RawMessage
	Artifacts []conversation.Artifact
	Error     string
	Ask       json.RawMessage
	Launch    *Launch
}

type Launch struct {
	Role  string
	Title string
	Task  string
}

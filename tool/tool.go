// Package tool is what a runner calls tools through: a user's set of tools, each described, prepared and invoked. A
// tool does its work and says what came of it; what reaches the conversation's log, a question to the user or a
// subagent launched among it, the runner appends. Built-in tools and MCP servers are sets of their own.
package tool

import (
	"context"
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
)

// Tools gives the set of tools a user's turns call.
type Tools func(ctx context.Context, user string) (Set, error)

type Set interface {
	Specs() []Spec
	// Unavailable are the providers whose tools could not be listed now.
	Unavailable() []string
	// Content is what a call touches, for a permission rule to match; only a tool whose spec says so has one.
	Content(ctx context.Context, c Call) (policy.Content, error)
	// Prepare plans a call before it is decided on; a tool with no plan gives none.
	Prepare(ctx context.Context, c Call) (Plan, error)
	// Invoke runs a call; its failure is its result.
	Invoke(ctx context.Context, c Call) Result
}

type Spec struct {
	Name        string
	Provider    string
	Description string
	Schema      map[string]any
	Annotations Annotations
	// Content says each call has content for permission rules.
	Content bool
	// Serial tools run alone, even when they only read.
	Serial bool
}

type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// OpenWorld is true unless the tool says otherwise.
func (a Annotations) OpenWorld() bool { return a.OpenWorldHint == nil || *a.OpenWorldHint }

// Caller is the conversation a call is made in.
type Caller struct {
	User        string
	Thread      string
	Workspace   string
	Environment string
	// Root is the workspace's folder.
	Root     string
	Subagent bool
	Role     string
	Item     string
	// Deferred are the tools offered by name only, which load_tools loads.
	Deferred []string
}

type Call struct {
	Caller
	Tool      string
	Arguments map[string]any
	// Prepared is the plan the call was decided with, which it runs by.
	Prepared json.RawMessage
}

// Plan is how a call will run: confined to the sandbox, or out of it.
type Plan struct {
	Data      json.RawMessage
	Sandboxed bool
	Escalated bool
}

// Result is one of: an output, an error, a question the call waits on the user to answer, or a subagent to launch.
type Result struct {
	Output    json.RawMessage
	Artifacts []conversation.Artifact
	Error     string
	Ask       json.RawMessage
	Launch    *Launch
}

// Launch is a subagent's conversation to start with a task; the call ends once it is started, and the subagent reports
// to the turn that launched it.
type Launch struct {
	Role  string
	Title string
	Task  string
}

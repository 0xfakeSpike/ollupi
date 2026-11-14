// Package subagent gives an agent the tools that launch subagents. A call does not run the subagent: it writes the
// subagent's conversation and its first turn, queued, and waits on that conversation, which the orchestrator starts
// and runs as it runs any, and whose final answer comes back as the call's result.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// ProviderName is the provider of the tools that launch subagents.
const ProviderName = "subagents"

const (
	// General and Browser are the kinds of subagent. Each is the name of the tool that launches it, and of the role
	// that its subagent runs as.
	General = "general-sub-agent"
	Browser = "browser-sub-agent"
)

// A Kind is how the tool that launches a kind of subagent shows: its title to the user, and its description to the
// launching agent, which tells it when to launch the kind; the tool adds how launching works.
type Kind struct {
	Title, Description string
}

// Kinds are the kinds of subagent, by name.
var Kinds = map[string]Kind{
	General: {
		Title: "Subagent",
		Description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
			"For a task in the built-in browser, launch " + Browser + " instead. " +
			"To work on independent tasks at the same time, call " + General + " several times in the same response; the subagents run in parallel.",
	},
	Browser: {
		Title: "Browser subagent",
		Description: "Launch a browser subagent: an agent that does one task in the built-in browser (opening pages, reading them, clicking and typing) and reports back. " +
			"It has only the browser's tools. Use it for browser work of many steps, so the pages it reads stay out of this conversation. " +
			"Browser subagents share the one browser, so launch one at a time.",
	},
}

// A Call is what a launch tool is called with: the task for the subagent.
type Call struct {
	Description string `json:"description" jsonschema:"A short (3-5 word) description of the task, shown to the user."`
	Prompt      string `json:"prompt" jsonschema:"The complete task for the subagent, self-contained."`
}

func (Call) Constrain(s *jsonschema.Schema) {
	s.Properties["description"].MinLength, s.Properties["description"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(80)
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(20000)
}

// Tools provides a tool for each kind of subagent.
type Tools struct {
	db *db.DB
}

func New(store *db.DB) *Tools { return &Tools{db: store} }

func (*Tools) Name() (string, string) { return ProviderName, "Launches subagents." }

const launching = "It knows nothing of this conversation: put everything it needs in prompt, including what its report must contain. " +
	"The turn waits until every subagent launched in the response has reported, and each call returns that subagent's final report. Subagents cannot launch subagents."

func (t *Tools) ToolList(context.Context) ([]*tools.Spec, error) {
	specs := make([]*tools.Spec, 0, len(Kinds))
	for name, k := range Kinds {
		specs = append(specs, &tools.Spec{
			Name:        name,
			Description: k.Description + " " + launching,
			InputSchema: tools.InputSchema[Call](),
			Annotations: &tools.Annotations{Title: k.Title, ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
		})
	}
	return specs, nil
}

// Invoke queues the subagent of the kind whose tool was called, and waits on its conversation.
func (t *Tools) Invoke(ctx context.Context, caller tools.Caller, name string, arguments map[string]any) (tools.Result, error) {
	if _, ok := Kinds[name]; !ok {
		return tools.Result{}, fmt.Errorf("%q launches no subagent", name)
	}
	c, err := tools.DecodeArguments[Call](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Description) == "" {
		return tools.Result{}, fmt.Errorf("description and prompt must not be blank")
	}
	item := caller.Item
	if item == "" {
		return tools.Result{}, fmt.Errorf("a subagent is launched only by a call of a turn")
	}
	child, err := t.db.QueueSubagent(caller.ThreadID, item, c.Description, db.TurnRow{Input: c.Prompt, Agent: name})
	if err != nil {
		return tools.Result{}, err
	}
	request, err := json.Marshal(c)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Await: &tools.Await{By: tools.AwaitAgent, Request: request, Handle: child.ID}}, nil
}

// A Progress is where a subagent that a call launched stands.
type Progress int

const (
	// Queued is a subagent whose turn waits to start.
	Queued Progress = iota
	// Running is a subagent whose turn has started and not ended.
	Running
	// Ended is a subagent whose turn ended: completed with a final answer, failed or stopped.
	Ended
)

// Report is where the subagent the call launched stands, with its conversation, and once it ended the call completed
// with its report: the subagent's final answer, or why it has none.
func Report(store *db.DB, call db.ItemRow) (db.ItemRow, Progress, db.ThreadRow, error) {
	child, err := store.ThreadState(call.Await.Handle)
	if err != nil {
		return db.ItemRow{}, 0, db.ThreadRow{}, fmt.Errorf("subagent: thread %s: %w", call.Await.Handle, err)
	}
	if _, active := child.ActiveTurn(); active {
		return db.ItemRow{}, Running, child.Thread, nil
	}
	if len(child.QueuedTurns()) > 0 {
		return db.ItemRow{}, Queued, child.Thread, nil
	}
	if child.Last == nil {
		return db.ItemRow{}, 0, child.Thread, fmt.Errorf("subagent: thread %s has no turn", child.Thread.ID)
	}
	ended := *child.Last
	now := time.Now().UTC()
	call.CompletedAt = &now
	switch ended.Status {
	case db.TurnCompleted:
		answer, err := store.FinalAnswer(child.Thread.ID, ended.ID)
		if err != nil {
			return db.ItemRow{}, 0, child.Thread, fmt.Errorf("subagent: %s completed turn %s without a final answer: %w", child.Thread.ID, ended.ID, err)
		}
		result, _, err := db.ResultOf(tools.Result{Output: answer})
		if err != nil {
			return db.ItemRow{}, 0, child.Thread, err
		}
		call.Status, call.Result = db.ItemCompleted, result
	case db.TurnInterrupted:
		call.Status, call.Error = db.ItemFailed, "the subagent was stopped: "+ended.Error
	default:
		call.Status, call.Error = db.ItemFailed, "the subagent failed: "+ended.Error
	}
	return call, Ended, child.Thread, nil
}

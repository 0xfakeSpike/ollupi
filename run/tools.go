package run

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// cutOffCall is why no call of a response the output limit cut off runs: the last may be incomplete, and the model
// would read a missing piece as a failed call instead of a request too long for one response.
const cutOffCall = "not run: the response reached the model's output limit before it was complete; do this in smaller steps, such as a long file written in parts"

// decide is what a call the model made comes to, as the response is kept: a call to run, one that asks the user
// first, or one that ends at once, refused or not runnable.
func (r *Runner) decide(ctx context.Context, s *step, response string, call llm.ToolCall, cutOff bool) []conversation.Entry {
	item := uuid.NewString()
	spec := s.specs[call.Name]
	tc := conversation.ToolCall{Call: call.ID, Tool: call.Name, Server: spec.Provider, Arguments: call.Arguments, Response: response}
	tc.Annotations, _ = json.Marshal(spec.Annotations) // plain data: it cannot fail
	entries := []conversation.Entry{{Turn: s.turn.ID, Item: item}}
	end := func(reason string) []conversation.Entry {
		entries[0].Body = tc
		return append(entries, conversation.Entry{Turn: s.turn.ID, Item: item, Body: conversation.ToolResult{Error: reason}})
	}
	if cutOff {
		return end(cutOffCall)
	}
	if reason := unavailable(s, call.Name); reason != "" {
		return end(reason)
	}
	args, err := arguments(call.Arguments)
	if err != nil {
		return end(err.Error())
	}
	c := tool.Call{Caller: s.caller(item), Tool: call.Name, Arguments: args}
	pc := policy.Call{Tool: call.Name, ReadOnly: spec.Annotations.ReadOnlyHint, OpenWorld: spec.Annotations.OpenWorld()}
	if spec.Content {
		if pc.Content, err = s.set.Content(ctx, c); err != nil {
			return end(err.Error())
		}
		pc.HasContent = true
	}
	plan, err := s.set.Prepare(ctx, c)
	if err != nil {
		return end(err.Error())
	}
	tc.Prepared, pc.Prepared, pc.Sandboxed, pc.Escalated = plan.Data, plan.Data, plan.Sandboxed, plan.Escalated
	entries[0].Body = tc
	switch d := s.checker.Decide(pc, s.v); d.Verdict {
	case policy.Deny:
		return append(entries, conversation.Entry{Turn: s.turn.ID, Item: item, Body: conversation.ToolResult{Error: d.Reason}})
	case policy.Ask:
		return append(entries, conversation.Entry{Turn: s.turn.ID, Item: item, Body: d.Asked(uuid.NewString(), pc)})
	}
	return entries
}

func unavailable(s *step, name string) string {
	sent := slices.ContainsFunc(s.sent, func(t prompt.Tool) bool { return t.Name == name })
	deferred := slices.ContainsFunc(s.deferred, func(t prompt.Tool) bool { return t.Name == name })
	switch {
	case sent:
		return ""
	case deferred:
		return fmt.Sprintf("tool %q is not loaded yet; call %s with its name first", name, agent.LoadToolsTool)
	}
	return fmt.Sprintf("tool %q is not available in this turn; use an exact tool name from the tool list", name)
}

func arguments(raw string) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("arguments are not a valid JSON object: %v", err)
	}
	if args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object, not null")
	}
	return args, nil
}

func (r *Runner) skip(ctx context.Context, s *step, a agent.SkipTools) error {
	entries := make([]conversation.Entry, len(a.Calls))
	for i, it := range a.Calls {
		entries[i] = conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolResult{Error: "skipped: " + a.Reason}}
	}
	return r.append(ctx, s.th.ID, entries...)
}

const maxParallelCalls = 8

// runTools runs the first of calls, or the calls that only read at the head of them, together; the rest wait for the
// next step, which may find tool use stopped.
func (r *Runner) runTools(ctx context.Context, s *step, calls []conversation.Item) error {
	n := 1
	for parallel(s, calls[0]) && n < min(len(calls), maxParallelCalls) && parallel(s, calls[n]) {
		n++
	}
	group := calls[:n]
	started := make([]conversation.Entry, len(group))
	for i, it := range group {
		started[i] = conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolStarted{}}
	}
	if err := r.append(ctx, s.th.ID, started...); err != nil {
		return err
	}
	results := make([]tool.Result, len(group))
	var g errgroup.Group
	for i, it := range group {
		g.Go(func() error {
			args, _ := arguments(it.Call.Arguments) // decide read them before the call was kept
			results[i] = s.set.Invoke(ctx, tool.Call{Caller: s.caller(it.ID), Tool: it.Call.Tool, Arguments: args, Prepared: it.Call.Prepared})
			return nil
		})
	}
	g.Wait() // a call's failure is its result
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.st.Update(ctx, func(tx store.Tx) error {
		for i, it := range group {
			if err := r.keep(tx, s, it, results[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func parallel(s *step, it conversation.Item) bool {
	spec := s.specs[it.Call.Tool]
	return spec.Annotations.ReadOnlyHint && !spec.Serial
}

// keep appends what a call came to: its output or error, the question it waits on the user to answer, or the subagent
// it launched.
func (r *Runner) keep(tx store.Tx, s *step, it conversation.Item, res tool.Result) error {
	entry := conversation.Entry{Turn: s.turn.ID, Item: it.ID}
	switch {
	case res.Ask != nil:
		entry.Body = conversation.InputAsked{Request: res.Ask}
	case res.Launch != nil:
		return launch(tx, s, it, *res.Launch)
	case res.Error != "":
		entry.Body = conversation.ToolResult{Error: res.Error}
	default:
		entry.Body = conversation.ToolResult{Output: res.Output, Artifacts: res.Artifacts}
		if err := s.window.CheckArtifacts(res.Artifacts); err != nil {
			entry.Body = conversation.ToolResult{Error: "apply tool context: " + err.Error()}
		}
	}
	_, err := tx.Append(s.th.ID, entry)
	return err
}

// launch starts a subagent's conversation with its task, on the model of the turn that launched it; the scheduler
// finds it runnable, and its ending reports back.
func launch(tx store.Tx, s *step, it conversation.Item, l tool.Launch) error {
	now := time.Now().UTC()
	child := store.Thread{User: s.th.User, Thread: user.Thread{ID: uuid.NewString(), Title: l.Title, Workspace: s.th.Workspace,
		Environment: s.th.Environment, Parent: s.th.ID, ParentItem: it.ID, Created: now, Updated: now}}
	if err := tx.CreateThread(child); err != nil {
		return err
	}
	turn := uuid.NewString()
	_, err := tx.Append(child.ID,
		conversation.Entry{Turn: turn, Body: conversation.TurnQueued{Model: s.turn.Model, Role: l.Role}},
		conversation.Entry{Turn: turn, Item: uuid.NewString(), Body: conversation.UserMessage{Text: l.Task}})
	if err != nil {
		return err
	}
	output, _ := json.Marshal(map[string]string{"thread": child.ID}) // a string: it cannot fail
	_, err = tx.Append(s.th.ID,
		conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ChildLaunched{Child: child.ID}},
		conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolResult{Output: output}})
	return err
}

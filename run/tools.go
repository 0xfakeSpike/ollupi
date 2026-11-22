package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const cutOffCall = "not run: the response reached the model's output limit before it was complete; do this in smaller steps, such as a long file written in parts"

func (r *Runner) decide(ctx context.Context, s *step, response string, call llm.ToolCall, cutOff bool) []conversation.Entry {
	item := uuid.NewString()
	spec := s.specs[call.Name]
	tc := conversation.ToolCall{Call: call.ID, Tool: call.Name, Server: spec.Provider, Arguments: call.Arguments, Response: response}
	tc.Annotations, _ = json.Marshal(spec.Annotations)
	entries := []conversation.Entry{{Turn: s.turn.ID, Item: item}}
	end := func(reason string) []conversation.Entry {
		entries[0].Body = tc
		return append(entries, conversation.Entry{Turn: s.turn.ID, Item: item, Body: conversation.ToolResult{Error: reason}})
	}
	if cutOff {
		return end(cutOffCall)
	}
	if reason := unavailableReason(s, call.Name); reason != "" {
		return end(reason)
	}
	args, err := parseArguments(call.Arguments)
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

func unavailableReason(s *step, name string) string {
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

func parseArguments(raw string) (map[string]any, error) {
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

func (r *Runner) runTools(ctx context.Context, s *step, calls []conversation.Item) error {
	if background(s, calls[0]) {
		return r.launchTask(ctx, s, calls[0])
	}
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
			args, _ := parseArguments(it.Call.Arguments)
			results[i] = s.set.Invoke(ctx, tool.Call{Caller: s.caller(it.ID), Tool: it.Call.Tool, Arguments: args, Prepared: it.Call.Prepared})
			return nil
		})
	}
	g.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.st.Update(ctx, func(tx store.Tx) error {
		for i, it := range group {
			if err := r.recordResult(tx, s, it, results[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func parallel(s *step, it conversation.Item) bool {
	spec := s.specs[it.Call.Tool]
	return spec.Annotations.ReadOnlyHint && !spec.Serial && !background(s, it)
}

func background(s *step, it conversation.Item) bool {
	args, _ := parseArguments(it.Call.Arguments)
	return s.specs[it.Call.Tool].InBackground(args)
}

type Started struct {
	Task string `json:"task"`
	Note string `json:"note"`
}

const startedNote = "It runs in the background; its result comes back to this conversation as a report when it ends. " +
	"Go on with other work, or end your reply to wait for it."

func (r *Runner) launchTask(ctx context.Context, s *step, it conversation.Item) error {
	args, _ := parseArguments(it.Call.Arguments)
	call, _ := json.Marshal(tool.Call{Caller: s.caller(it.ID), Tool: it.Call.Tool, Arguments: args, Prepared: it.Call.Prepared})
	task := store.Task{ID: uuid.NewString(), User: s.th.User, Thread: s.th.ID, Turn: s.turn.ID, Item: it.ID, Call: call, Created: time.Now().UTC()}
	output, _ := json.Marshal(Started{Task: task.ID, Note: startedNote})
	return r.st.Update(ctx, func(tx store.Tx) error {
		if err := tx.CreateTask(task); err != nil {
			return err
		}
		_, err := tx.Append(s.th.ID,
			conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolStarted{}},
			conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.TaskLaunched{Task: task.ID}},
			conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolResult{Output: output}})
		return err
	})
}

const strandedTask = "the task was cut off when the process running it stopped; it may have taken effect in part or in full, so check before trying it again"

func (r *Runner) Task(ctx context.Context, id string) error {
	var t store.Task
	if err := r.st.Read(ctx, func(rd store.Reader) (err error) { t, err = rd.Task(id); return err }); err != nil {
		return err
	}
	if t.Started != nil {
		return r.st.Update(ctx, func(tx store.Tx) error { return ReportTask(tx, id, tool.Result{Error: strandedTask}) })
	}
	if err := r.st.Update(ctx, func(tx store.Tx) error { return tx.StartTask(id, time.Now().UTC()) }); err != nil {
		return err
	}
	var c tool.Call
	if err := json.Unmarshal(t.Call, &c); err != nil {
		return fmt.Errorf("run: the call of task %s: %w", id, err)
	}
	set, err := r.tools(ctx, t.User)
	if err != nil {
		return fmt.Errorf("run: the tools of the user: %w", err)
	}
	res := set.Invoke(ctx, c)
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.st.Update(ctx, func(tx store.Tx) error { return ReportTask(tx, id, res) })
}

func ReportTask(tx store.Tx, id string, res tool.Result) error {
	t, err := tx.Task(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.DeleteTask(id); err != nil {
		return err
	}
	_, err = tx.Append(t.Thread, conversation.Entry{Turn: t.Turn, Item: uuid.NewString(),
		Body: conversation.TaskReported{Launch: t.Item, Task: id, Output: res.Output, Error: res.Error}})
	return err
}

func (r *Runner) recordResult(tx store.Tx, s *step, it conversation.Item, res tool.Result) error {
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

type Launched struct {
	Subagent    string `json:"subagent"`
	Description string `json:"description"`
	Note        string `json:"note"`
}

func launch(tx store.Tx, s *step, it conversation.Item, l tool.Launch) error {
	now := time.Now().UTC()
	// The child works at the permission settings of the thread that launched it, and keeps its own from then on.
	child := store.Thread{User: s.th.User, Thread: account.Thread{ID: uuid.NewString(), Title: l.Title, Workspace: s.th.Workspace,
		Environment: s.th.Environment, Settings: s.th.Settings, Parent: s.th.ID, ParentItem: it.ID, Role: l.Role, Created: now, Updated: now}}
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
	detached := agent.Detached(l.Role)
	note := "It works on its own; its report comes back to this conversation when it ends."
	if detached {
		note = "It works on its own and does not report back; the user follows it in its own conversation."
	}
	output, _ := json.Marshal(Launched{Subagent: child.ID, Description: l.Title, Note: note})
	_, err = tx.Append(s.th.ID,
		conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ChildLaunched{Child: child.ID, Detached: detached}},
		conversation.Entry{Turn: s.turn.ID, Item: it.ID, Body: conversation.ToolResult{Output: output}})
	return err
}

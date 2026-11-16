package role

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"golang.org/x/sync/errgroup"
)

// maxParallelCalls bounds the read-only calls of one answer that run at once.
const maxParallelCalls = 8

// callTools decides and runs the turn's pending tool calls in order, each once the calls before it ran: read-only
// calls in a row run together, any other alone. A call that asks for approval leaves the calls after it pending, for
// the turn to wait; once tool use stopped, as the role's stop says, the calls left are skipped.
func (rt *Runtime) callTools(ctx context.Context) error {
	var batch []db.ItemRow
	for i, it := range rt.items {
		if !it.IsCall() || it.Status != db.ItemPending {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		readOnly := rt.readOnly(it.Tool)
		if !readOnly { // a call that is not read-only runs once the reads before it ran
			if err := rt.runCalls(ctx, batch); err != nil {
				return err
			}
			batch = nil
		}
		if stop := rt.stop(len(batch)); stop != "" {
			if err := rt.fail(it, "skipped: "+stop); err != nil {
				return err
			}
			continue
		}
		call, err := rt.handleCall(ctx, it)
		switch {
		case err != nil:
			return err
		case call != nil && readOnly:
			batch = append(batch, *call)
		case call != nil:
			if err := rt.runCalls(ctx, []db.ItemRow{*call}); err != nil {
				return err
			}
		case rt.items[i].WaitsForApproval(): // it holds the calls after it: the pass ends with it
			return rt.runCalls(ctx, batch)
		}
	}
	return rt.runCalls(ctx, batch)
}

func (rt *Runtime) readOnly(tool string) bool {
	d, ok := rt.tool(tool)
	return ok && d.Annotations.ReadOnlyHint && !d.Serial
}

// runCalls runs the calls together, up to maxParallelCalls at once, and records what each returned.
func (rt *Runtime) runCalls(ctx context.Context, calls []db.ItemRow) error {
	if len(calls) == 0 {
		return nil
	}
	for i := range calls {
		calls[i].Status = db.ItemInProgress
		stored, err := rt.record(calls[i])
		if err != nil {
			return err
		}
		calls[i] = stored
	}
	results := make([]tools.Result, len(calls))
	var g errgroup.Group
	g.SetLimit(maxParallelCalls)
	for i, it := range calls {
		g.Go(func() error {
			results[i] = rt.execute(ctx, it)
			return nil
		})
	}
	g.Wait() // execute returns no error: a call's failure is its result
	for i, it := range calls {
		if err := rt.finishTool(it, results[i]); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (rt *Runtime) execute(ctx context.Context, it db.ItemRow) tools.Result {
	args, _ := callArguments(it) // a call runs only once handleCall read its arguments
	started := time.Now()
	result := rt.Registry.InvokePrepared(ctx, rt.caller(it), it.Tool, args, it.PreparedExecution)
	if result.Err == nil {
		if err := rt.window.CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	// The error stays out of the log with the output: either can quote the files and commands it touched.
	slog.InfoContext(ctx, "tool call", "thread", rt.thread.ID, "turn", rt.turn.ID, "item", it.ID, "tool", it.Tool,
		"ms", time.Since(started).Milliseconds(), "failed", result.Err != nil, "awaits", result.Await != nil)
	return result
}

// finishTool records what a call's run returned: the await it waits on, or its result, completed or failed.
func (rt *Runtime) finishTool(it db.ItemRow, result tools.Result) error {
	if result.Err == nil && result.Await != nil {
		it.Await = result.Await
		_, err := rt.record(it)
		return err
	}
	stored, errText, err := db.ResultOf(result)
	if err != nil {
		// A result with no output: it cannot fail.
		stored, errText, _ = db.ResultOf(tools.Result{Err: fmt.Errorf("tool output cannot be recorded: %w", err)}.For(it.Tool))
	}
	it.Result, it.Error = stored, errText
	status := db.ItemCompleted
	if errText != "" {
		status = db.ItemFailed
	}
	_, err = rt.complete(it, status)
	return err
}

func (rt *Runtime) fail(it db.ItemRow, reason string) error {
	_, err := rt.complete(withError(it, reason), db.ItemFailed)
	return err
}

// caller is who makes the call that the item records: the run's role, in its conversation.
func (rt *Runtime) caller(it db.ItemRow) tools.Caller {
	t := rt.thread
	_, deferred := rt.Offer()
	names := make([]string, len(deferred))
	for i, d := range deferred {
		names[i] = d.Name
	}
	return tools.Caller{
		UserID: t.UserID, ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), EnvironmentID: string(t.EnvironmentID),
		Root: rt.workspace.RootPath, Subagent: t.ParentThreadID != "", Role: string(rt.role.name), Item: it.ID,
		Deferred: names,
	}
}

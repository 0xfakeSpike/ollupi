package role

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"golang.org/x/sync/errgroup"
)

const maxParallelCalls = 8

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
		if !readOnly {
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
		case rt.items[i].WaitsForApproval():
			return rt.runCalls(ctx, batch)
		}
	}
	return rt.runCalls(ctx, batch)
}

func (rt *Runtime) readOnly(tool string) bool {
	d, ok := rt.tool(tool)
	return ok && d.Annotations.ReadOnlyHint && !d.Serial
}

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
	result := rt.registry.InvokePrepared(ctx, rt.caller(it), it.Tool, args, it.PreparedExecution)
	if result.Err == nil {
		if err := rt.window.CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	return result
}

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

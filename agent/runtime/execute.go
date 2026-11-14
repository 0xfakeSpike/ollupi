package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// inspect prepares the call when the step says so, recording how it will run, and reads what it would do.
func (rt *Runtime) inspect(ctx context.Context, st Inspect) (Inspection, error) {
	it := st.Call
	if st.Prepare {
		prepared, err := rt.Registry.Prepare(ctx, it.Tool, st.Args)
		if err != nil {
			return Inspection{Err: err}, nil
		}
		if prepared != nil {
			it.PreparedExecution = prepared
			if _, err := rt.record(it); err != nil {
				return Inspection{}, err
			}
		}
	}
	content, hasContent, err := rt.Registry.PermissionContent(ctx, it.Tool, st.Args)
	return Inspection{Content: content, HasContent: hasContent, Err: err}, nil
}

// runCalls records what was decided on the pending calls, then runs the step's calls at the same time.
func (rt *Runtime) runCalls(ctx context.Context, st Calls) error {
	for _, it := range st.Records {
		if _, err := rt.record(it); err != nil {
			return err
		}
		if it.WaitsForApproval() {
			slog.Info("approval requested", "thread", rt.Caller.ThreadID, "turn", rt.Turn.ID, "item", it.ID, "tool", it.Tool)
		}
	}
	if len(st.Run) == 0 {
		return nil
	}
	jobs := slices.Clone(st.Run)
	results, err := rt.runJobs(ctx, st.Deferred, jobs)
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if err := rt.finishTool(j, results[i]); err != nil {
			return err
		}
	}
	return nil
}

func (rt *Runtime) runJobs(ctx context.Context, deferred []tools.ToolDescriptor, jobs []job) ([]tools.Result, error) {
	ctx = context.WithValue(ctx, deferredKey{}, deferred)
	for i := range jobs {
		jobs[i].it.Status = repo.ItemInProgress
		stored, err := rt.record(jobs[i].it)
		if err != nil {
			return nil, err
		}
		jobs[i].it = stored
	}
	results := make([]tools.Result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() { results[i] = rt.execute(ctx, j) })
	}
	wg.Wait()
	return results, nil
}

func (rt *Runtime) execute(ctx context.Context, j job) tools.Result {
	it := j.it
	ctx, span := tracer.Start(ctx, "execute_tool "+it.Tool, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"), attribute.String("gen_ai.tool.name", it.Tool),
		attribute.String("gen_ai.tool.call.id", it.CallID), attribute.String("ollupi.tool.server", it.Server)))
	defer span.End()
	started := time.Now()
	if j.background {
		span.SetAttributes(attribute.Bool("ollupi.tool.background", true))
		return rt.Host.Background(it, j.args)
	}
	result := rt.Registry.InvokePrepared(ctx, it.Tool, j.args, it.PreparedExecution)
	if result.Err == nil {
		if err := rt.Window.CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	span.SetAttributes(attribute.Bool("ollupi.tool.awaits", result.Await != nil))
	// The error stays out of the log and the trace with the output: either can quote the files and commands it touched.
	if result.Err != nil {
		span.SetStatus(codes.Error, "")
	}
	slog.InfoContext(ctx, "tool call", "thread", rt.Caller.ThreadID, "turn", rt.Turn.ID, "item", it.ID, "tool", it.Tool,
		"ms", time.Since(started).Milliseconds(), "failed", result.Err != nil, "awaits", result.Await != nil)
	return result
}

func (rt *Runtime) finishTool(j job, result tools.Result) error {
	it := j.it
	if result.Err == nil && result.Await != nil {
		it.Await = result.Await
		_, err := rt.record(it)
		return err
	}
	stored, errText, err := repo.ResultOf(result)
	if err != nil {
		stored, errText, err = repo.ResultOf(tools.Result{Err: fmt.Errorf("tool output cannot be recorded: %w", err)}.For(it.Tool))
		if err != nil {
			return err
		}
	}
	it.Result, it.Error = stored, errText
	status := repo.ItemCompleted
	if errText != "" {
		status = repo.ItemFailed
	}
	_, err = rt.record(completed(it, status))
	return err
}

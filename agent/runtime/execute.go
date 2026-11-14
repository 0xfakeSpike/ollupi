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
func (r *run) inspect(ctx context.Context, st Inspect) (Inspection, error) {
	it := st.Call
	if st.Prepare {
		prepared, err := r.registry.Prepare(ctx, it.Tool, st.Args)
		if err != nil {
			return Inspection{Err: err}, nil
		}
		if prepared != nil {
			it.PreparedExecution = prepared
			if _, err := r.record(it); err != nil {
				return Inspection{}, err
			}
		}
	}
	content, hasContent, err := r.registry.PermissionContent(ctx, it.Tool, st.Args)
	return Inspection{Content: content, HasContent: hasContent, Err: err}, nil
}

// runCalls records what was decided on the pending calls, then runs the step's calls at the same time.
func (r *run) runCalls(ctx context.Context, st Calls) error {
	for _, it := range st.Records {
		if _, err := r.record(it); err != nil {
			return err
		}
		if it.WaitsForApproval() {
			slog.Info("approval requested", "thread", r.t.ID, "turn", r.state.Turn.ID, "item", it.ID, "tool", it.Tool)
		}
	}
	jobs := slices.Clone(st.Run)
	for i := range jobs {
		jobs[i].Item.Status = repo.ItemInProgress
		stored, err := r.record(jobs[i].Item)
		if err != nil {
			return err
		}
		jobs[i].Item = stored
	}
	ctx = WithDeferred(ctx, st.Deferred)
	results := make([]tools.Result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() { results[i] = r.execute(ctx, j) })
	}
	wg.Wait()
	for i, j := range jobs {
		ended, err := Ended(j.Item, results[i])
		if err != nil {
			return err
		}
		if _, err := r.record(ended); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) execute(ctx context.Context, j Job) tools.Result {
	it := j.Item
	ctx, span := tracer.Start(ctx, "execute_tool "+it.Tool, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"), attribute.String("gen_ai.tool.name", it.Tool),
		attribute.String("gen_ai.tool.call.id", it.CallID), attribute.String("ollupi.tool.server", it.Server)))
	defer span.End()
	started := time.Now()
	if j.Background {
		span.SetAttributes(attribute.Bool("ollupi.tool.background", true))
		return r.queueBackground(it, j.Args)
	}
	result := r.registry.InvokePrepared(ctx, it.Tool, j.Args, it.PreparedExecution)
	if result.Err == nil {
		if err := r.state.Window.CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	span.SetAttributes(attribute.Bool("ollupi.tool.awaits", result.Await != nil))
	// The error stays out of the log and the trace with the output: either can quote the files and commands it touched.
	if result.Err != nil {
		span.SetStatus(codes.Error, "")
	}
	slog.InfoContext(ctx, "tool call", "thread", r.t.ID, "turn", r.state.Turn.ID, "item", it.ID, "tool", it.Tool,
		"ms", time.Since(started).Milliseconds(), "failed", result.Err != nil, "awaits", result.Await != nil)
	return result
}

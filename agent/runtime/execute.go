package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// skipped is why a pending call of a turn that stopped did not run.
const skipped = "skipped: the turn stopped before this call was executed"

// callTools runs the turn's pending tool calls in order, as the role's permissions decide each one, once the calls
// before it ran: read-only calls in a row run together, any other alone. A call that needs approval makes the turn
// wait, and the calls after it stay pending; a call that stops the turn, as one the user declined does, leaves those
// after it skipped, and the model is told to give its final answer. It reports whether it gave it.
func (rt *Runtime) callTools(ctx context.Context) (bool, error) {
	halt := ""
	var batch []job
	// unapproved counts the batched calls toolCalls does not count yet: pending ones that no approval made count.
	unapproved := 0
	run := func(jobs []job) error {
		if len(jobs) == 0 {
			return nil
		}
		results, err := rt.runJobs(ctx, jobs)
		if err != nil {
			return err
		}
		for i, j := range jobs {
			h, err := rt.finishTool(j, results[i])
			if err != nil {
				return err
			}
			if halt == "" {
				halt = h
			}
		}
		return ctx.Err()
	}
	flush := func() error {
		jobs := batch
		batch, unapproved = nil, 0
		return run(jobs)
	}
	for _, it := range slices.Clone(rt.Role.Items()) {
		if !it.IsCall() || it.Status != db.ItemPending {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		readOnly := rt.readOnly(it.Tool)
		if !readOnly {
			if err := flush(); err != nil {
				return false, err
			}
		}
		if halt != "" {
			if err := rt.fail(it, skipped); err != nil {
				return false, err
			}
			continue
		}
		d, err := rt.handleCall(ctx, it, unapproved)
		if err != nil {
			return false, err
		}
		switch {
		case d.wait:
			return false, flush()
		case d.halt != "":
			if err := flush(); err != nil {
				return false, err
			}
			if halt == "" {
				halt = d.halt
			}
		case d.job != nil && readOnly:
			if it.Approval == nil {
				unapproved++
			}
			batch = append(batch, *d.job)
		case d.job != nil:
			if err := run([]job{*d.job}); err != nil {
				return false, err
			}
		}
	}
	if err := flush(); err != nil {
		return false, err
	}
	if rt.waiting() || halt == "" {
		return false, nil
	}
	return true, rt.finish(ctx, halt)
}

func (rt *Runtime) runJobs(ctx context.Context, jobs []job) ([]tools.Result, error) {
	for i := range jobs {
		jobs[i].it.Status = db.ItemInProgress
		stored, err := rt.record(jobs[i].it)
		if err != nil {
			return nil, err
		}
		jobs[i].it = stored
	}
	results := make([]tools.Result, len(jobs))
	var g errgroup.Group
	g.SetLimit(maxParallelCalls)
	for i, j := range jobs {
		g.Go(func() error {
			results[i] = rt.execute(ctx, j)
			return nil
		})
	}
	return results, g.Wait()
}

func (rt *Runtime) execute(ctx context.Context, j job) tools.Result {
	it := j.it
	started := time.Now()
	result := rt.Registry.InvokePrepared(ctx, rt.caller(it), it.Tool, j.args, it.PreparedExecution)
	if result.Err == nil {
		if err := rt.Role.Window().CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	// The error stays out of the log with the output: either can quote the files and commands it touched.
	slog.InfoContext(ctx, "tool call", "thread", rt.Role.Thread().ID, "turn", rt.Role.Turn().ID, "item", it.ID, "tool", it.Tool,
		"ms", time.Since(started).Milliseconds(), "failed", result.Err != nil, "awaits", result.Await != nil)
	return result
}

func (rt *Runtime) finishTool(j job, result tools.Result) (string, error) {
	it := j.it
	if result.Err == nil && result.Await != nil {
		it.Await = result.Await
		_, err := rt.record(it)
		return "", err
	}
	stored, errText, err := db.ResultOf(result)
	if err != nil {
		stored, errText, err = db.ResultOf(tools.Result{Err: fmt.Errorf("tool output cannot be recorded: %w", err)}.For(it.Tool))
		if err != nil {
			return "", err
		}
	}
	it.Result, it.Error = stored, errText
	status := db.ItemCompleted
	if errText != "" {
		status = db.ItemFailed
	}
	if _, err := rt.complete(it, status); err != nil {
		return "", err
	}
	if status == db.ItemCompleted {
		return "", nil
	}
	return rt.haltAfterFailures(errText), nil
}

func (rt *Runtime) fail(it db.ItemRow, reason string) error {
	_, err := rt.complete(withError(it, reason), db.ItemFailed)
	return err
}

// refuse fails a call that cannot run for reason, which stops the turn only after too many failures in a row.
func (rt *Runtime) refuse(it db.ItemRow, reason string) (decision, error) {
	if err := rt.fail(it, reason); err != nil {
		return decision{}, err
	}
	return decision{halt: rt.haltAfterFailures(reason)}, nil
}

// haltAfterFailures returns why the turn stops when the latest tool calls all failed, or "" while it goes on.
func (rt *Runtime) haltAfterFailures(last string) string {
	if n := rt.consecutiveFailures(); n >= maxConsecutiveFailures {
		return fmt.Sprintf("%d consecutive invalid or failed tool calls; last: %s", n, last)
	}
	return ""
}

package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

type callActionKind int

const (
	actionRun callActionKind = iota
	actionRunReadOnly
	actionSkip
	actionWait
	actionHalt
)

type callAction struct {
	kind   callActionKind
	item   repo.ItemRow
	job    *job
	reason string
}

func (rt *Runtime) executePlan(ctx context.Context, plan []callAction) (bool, error) {
	var batch []job
	halt := ""
	flush := func() (string, error) {
		if len(batch) == 0 {
			return "", nil
		}
		var h string
		results, err := rt.runJobs(ctx, batch)
		if err != nil {
			return "", err
		}
		for i, j := range batch {
			next, err := rt.finishTool(j, results[i])
			if err != nil {
				return h, err
			}
			if h == "" {
				h = next
			}
		}
		batch = batch[:0]
		return h, ctx.Err()
	}
	runOne := func(j job) (string, error) {
		jobs := []job{j}
		results, err := rt.runJobs(ctx, jobs)
		if err != nil {
			return "", err
		}
		h, err := rt.finishTool(jobs[0], results[0])
		return h, errors.Join(err, ctx.Err())
	}
	for _, a := range plan {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		switch a.kind {
		case actionRunReadOnly:
			if halt != "" {
				if err := rt.fail(a.item, "skipped: the turn stopped before this call was executed"); err != nil {
					return false, err
				}
				continue
			}
			batch = append(batch, *a.job)
		case actionRun:
			h, err := flush()
			if err != nil {
				return false, err
			}
			if halt == "" {
				halt = h
			}
			if halt != "" {
				if err := rt.fail(a.item, "skipped: the turn stopped before this call was executed"); err != nil {
					return false, err
				}
				continue
			}
			h, err = runOne(*a.job)
			if err != nil {
				return false, err
			}
			if halt == "" {
				halt = h
			}
		case actionSkip:
			if _, err := flush(); err != nil {
				return false, err
			}
			if err := rt.fail(a.item, a.reason); err != nil {
				return false, err
			}
		case actionWait:
			if _, err := flush(); err != nil {
				return false, err
			}
			return false, nil
		case actionHalt:
			if _, err := flush(); err != nil {
				return false, err
			}
			halt = a.reason
		}
	}
	h, err := flush()
	if err != nil {
		return false, err
	}
	if halt == "" {
		halt = h
	}
	if rt.waiting() {
		return false, nil
	}
	if halt != "" {
		return true, rt.finish(ctx, halt)
	}
	return false, nil
}

func (rt *Runtime) runJobs(ctx context.Context, jobs []job) ([]tools.Result, error) {
	ctx = context.WithValue(ctx, deferredKey{}, rt.deferred)
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
	started := time.Now()
	result := rt.Registry.InvokePrepared(ctx, it.Tool, j.args, it.PreparedExecution)
	if result.Err == nil {
		if err := rt.Window.CheckArtifacts(result.Artifacts); err != nil {
			result = tools.Result{Err: fmt.Errorf("apply tool context: %w", err)}.For(it.Tool)
		}
	}
	// The error stays out of the log with the output: either can quote the files and commands it touched.
	slog.Info("tool call", "thread", rt.Caller.ThreadID, "turn", rt.Turn.ID, "item", it.ID, "tool", it.Tool,
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
	stored, errText, err := repo.ResultOf(result)
	if err != nil {
		stored, errText, err = repo.ResultOf(tools.Result{Err: fmt.Errorf("tool output cannot be recorded: %w", err)}.For(it.Tool))
		if err != nil {
			return "", err
		}
	}
	it.Result, it.Error = stored, errText
	status := repo.ItemCompleted
	if errText != "" {
		status = repo.ItemFailed
	}
	if _, err := rt.complete(it, status); err != nil {
		return "", err
	}
	if status == repo.ItemCompleted {
		return "", nil
	}
	return rt.haltAfterFailures(errText), nil
}

func (rt *Runtime) fail(it repo.ItemRow, reason string) error {
	_, err := rt.complete(withError(it, reason), repo.ItemFailed)
	return err
}

func (rt *Runtime) refuse(it repo.ItemRow, reason string) (string, error) {
	if err := rt.fail(it, reason); err != nil {
		return "", err
	}
	return rt.haltAfterFailures(reason), nil
}

// haltAfterFailures returns why the turn stops when the latest tool calls all failed, or "" while it goes on.
func (rt *Runtime) haltAfterFailures(last string) string {
	if n := rt.consecutiveFailures(); n >= maxConsecutiveFailures {
		return fmt.Sprintf("%d consecutive invalid or failed tool calls; last: %s", n, last)
	}
	return ""
}

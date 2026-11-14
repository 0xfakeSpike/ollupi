package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// startQueued starts the background calls that wait to start.
func (o *Orchestrator) startQueued() {
	calls, err := o.cfg.DB.QueuedBackgroundCalls()
	if err != nil {
		o.fault("", "", fmt.Errorf("queued background calls could not be listed: %w", err))
		return
	}
	for _, call := range calls {
		started, err := o.cfg.DB.StartBackgroundCall(call.ID, time.Now())
		if err != nil {
			slog.Error("background call not started", "thread", call.ThreadID, "background", call.ID, "err", err)
			continue
		}
		if started {
			o.startBackground(call)
		}
	}
}

// startBackground runs a call that started in a goroutine of its own, within the time limit; its result comes back
// to its thread when it ends.
func (o *Orchestrator) startBackground(call repo.BackgroundCallRow) {
	limit := fmt.Errorf("the background call reached its time limit of %s", o.cfg.BackgroundLimit)
	ctx, cancel := context.WithTimeoutCause(o.ctx, o.cfg.BackgroundLimit, limit)
	o.backgroundMu.Lock()
	o.backgroundCancels[call.ID] = cancel
	o.backgroundMu.Unlock()
	o.done.Add(1)
	go func() {
		defer o.done.Done()
		defer o.forgetBackground(call.ID)
		slog.Info("background call started", "thread", call.ThreadID, "item", call.ItemID, "background", call.ID, "tool", call.Tool)
		o.endBackground(ctx, call, limit, o.invokeBackground(ctx, call))
	}()
}

// invokeBackground calls the tool of a background call as the call that started it would have: for its thread, with
// its user's tools, its arguments and how it was prepared to run.
func (o *Orchestrator) invokeBackground(ctx context.Context, call repo.BackgroundCallRow) tools.Result {
	failed := func(err error) tools.Result { return tools.Result{Err: err}.For(call.Tool) }
	t, err := o.cfg.DB.ThreadRow(call.ThreadID)
	if err != nil {
		return failed(err)
	}
	w, err := o.workspaceOf(t.WorkspaceID)
	if err != nil {
		return failed(err)
	}
	registry, err := o.registryFor(t.UserID)
	if err != nil {
		return failed(err)
	}
	it, err := o.cfg.DB.Item(call.ItemID)
	if err != nil {
		return failed(err)
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return failed(fmt.Errorf("decode the arguments: %w", err))
	}
	ctx = tools.WithCaller(ctx, tools.Caller{
		UserID: t.UserID, ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), EnvironmentID: string(t.EnvironmentID),
		Root: w.RootPath, Subagent: t.ParentThreadID != "",
	})
	return registry.InvokePrepared(ctx, call.Tool, args, it.PreparedExecution)
}

func (o *Orchestrator) endBackground(ctx context.Context, call repo.BackgroundCallRow, limit error, result tools.Result) {
	status, failure := repo.BackgroundCompleted, ""
	var stored *repo.ToolResult
	switch {
	case o.ctx.Err() != nil:
		status, failure = repo.BackgroundInterrupted, "the process stopped before the call ended"
	case errors.Is(context.Cause(ctx), limit):
		status, failure = repo.BackgroundFailed, limit.Error()
	case result.Err == nil && result.Await != nil:
		status, failure = repo.BackgroundFailed, "a call that waits on an answer cannot run in the background"
	default:
		var err error
		if stored, failure, err = repo.ResultOf(result); err != nil {
			stored, failure = nil, fmt.Sprintf("tool output cannot be recorded: %v", err)
		}
		if failure != "" {
			status = repo.BackgroundFailed
		}
	}
	ended, err := o.cfg.DB.EndBackgroundCall(call.ID, status, stored, failure, time.Now())
	if err != nil {
		slog.Error("background call not ended", "thread", call.ThreadID, "background", call.ID, "err", err)
		return
	}
	slog.Info("background call ended", "thread", call.ThreadID, "background", call.ID, "status", status, "recorded", ended)
	if ended && status != repo.BackgroundInterrupted {
		o.post(func() {
			o.deliverBackground(call.ThreadID)
			o.reconcile(call.ThreadID)
		})
	}
}

func (o *Orchestrator) forgetBackground(id string) {
	o.backgroundMu.Lock()
	cancel := o.backgroundCancels[id]
	delete(o.backgroundCancels, id)
	o.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelBackground ends a call that waits to start or runs as cancelled before it stops it, so that the result its tool may still return
// does not take the cancellation's place. It reports false for a call that had already ended.
func (o *Orchestrator) cancelBackground(id, why string) (bool, error) {
	ended, err := o.cfg.DB.EndBackgroundCall(id, repo.BackgroundCancelled, nil, why, time.Now())
	if err != nil || !ended {
		return false, err
	}
	o.forgetBackground(id)
	return true, nil
}

// cancelBackgroundOf cancels the calls that wait to start or run in a thread and its subagents' threads, as one that is archived
// or deleted needs none of them.
func (o *Orchestrator) cancelBackgroundOf(threadID, why string) error {
	ids, err := o.cfg.DB.UnendedBackgroundCalls(threadID)
	if err != nil {
		return fmt.Errorf("orchestrator: background calls of thread %s: %w", threadID, err)
	}
	for _, id := range ids {
		if _, err := o.cancelBackground(id, why); err != nil {
			return fmt.Errorf("orchestrator: cancel background call %s: %w", id, err)
		}
	}
	return nil
}

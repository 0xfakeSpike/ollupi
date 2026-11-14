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
	"github.com/google/uuid"
)

// startBackground starts a call of the thread in the background and returns at once with what the model is told: the
// call's id, under which its result comes back when it ends.
func (o *Orchestrator) startBackground(t repo.ThreadRow, caller tools.Caller, registry *tools.Registry, it repo.ItemRow, args map[string]any) tools.Result {
	raw, err := json.Marshal(args)
	if err != nil {
		return tools.Result{Err: fmt.Errorf("encode the arguments: %w", err)}.For(it.Tool)
	}
	call := repo.BackgroundCallRow{ID: uuid.NewString(), ThreadID: t.ID, ItemID: it.ID, Tool: it.Tool, Arguments: string(raw), StartedAt: time.Now()}
	if err := o.cfg.DB.StartBackgroundCall(t.UserID, call, o.cfg.BackgroundCalls); err != nil {
		return tools.Result{Err: err}.For(it.Tool)
	}
	limit := fmt.Errorf("the background call reached its time limit of %s", o.cfg.BackgroundLimit)
	ctx, cancel := context.WithTimeoutCause(tools.WithCaller(o.ctx, caller), o.cfg.BackgroundLimit, limit)
	o.backgroundMu.Lock()
	o.backgroundCancels[call.ID] = cancel
	o.backgroundMu.Unlock()
	o.done.Add(1)
	go func() {
		defer o.done.Done()
		defer o.forgetBackground(call.ID)
		slog.Info("background call started", "thread", t.ID, "item", it.ID, "background", call.ID, "tool", it.Tool)
		o.endBackground(ctx, call, limit, registry.InvokePrepared(ctx, it.Tool, args, it.PreparedExecution))
	}()
	return tools.Result{Output: map[string]any{
		"background": call.ID, "status": string(repo.BackgroundRunning),
		"note": "It runs in the background; its result comes back to this conversation when it ends.",
	}}
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

// cancelBackground ends a running call as cancelled before it stops it, so that the result its tool may still return
// does not take the cancellation's place. It reports false for a call that had already ended.
func (o *Orchestrator) cancelBackground(id, why string) (bool, error) {
	ended, err := o.cfg.DB.EndBackgroundCall(id, repo.BackgroundCancelled, nil, why, time.Now())
	if err != nil || !ended {
		return false, err
	}
	o.forgetBackground(id)
	return true, nil
}

// cancelBackgroundOf cancels the calls still running in a thread and its subagents' threads, as one that is archived
// or deleted needs none of them.
func (o *Orchestrator) cancelBackgroundOf(threadID, why string) error {
	ids, err := o.cfg.DB.RunningBackgroundCalls(threadID)
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

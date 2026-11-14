package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
)

// ToolCancelBackground stops a background call of the conversation.
const ToolCancelBackground = "cancel_background"

// startBackground starts a call of the thread in the background and returns at once with what the model is told: the
// call's id, under which its result comes back when it ends.
func (a *Agent) startBackground(t repo.ThreadRow, caller tools.Caller, registry *tools.Registry) func(repo.ItemRow, map[string]any) tools.Result {
	return func(it repo.ItemRow, args map[string]any) tools.Result {
		raw, err := json.Marshal(args)
		if err != nil {
			return tools.Result{Err: fmt.Errorf("encode the arguments: %w", err)}.For(it.Tool)
		}
		call := repo.BackgroundCallRow{ID: uuid.NewString(), ThreadID: t.ID, ItemID: it.ID, Tool: it.Tool, Arguments: string(raw), StartedAt: time.Now()}
		if err := a.cfg.DB.StartBackgroundCall(t.UserID, call, a.cfg.BackgroundCalls); err != nil {
			return tools.Result{Err: err}.For(it.Tool)
		}
		limit := fmt.Errorf("the background call reached its time limit of %s", a.cfg.BackgroundLimit)
		ctx, cancel := context.WithTimeoutCause(tools.WithCaller(a.ctx, caller), a.cfg.BackgroundLimit, limit)
		a.backgroundMu.Lock()
		a.backgroundCancels[call.ID] = cancel
		a.backgroundMu.Unlock()
		a.done.Add(1)
		go func() {
			defer a.done.Done()
			defer a.forgetBackground(call.ID)
			slog.Info("background call started", "thread", t.ID, "item", it.ID, "background", call.ID, "tool", it.Tool)
			a.endBackground(ctx, call, limit, registry.InvokePrepared(ctx, it.Tool, args, it.PreparedExecution))
		}()
		return tools.Result{Output: map[string]any{
			"background": call.ID, "status": string(repo.BackgroundRunning),
			"note": "It runs in the background; its result comes back to this conversation when it ends.",
		}}
	}
}

func (a *Agent) endBackground(ctx context.Context, call repo.BackgroundCallRow, limit error, result tools.Result) {
	status, failure := repo.BackgroundCompleted, ""
	var stored *repo.ToolResult
	switch {
	case a.ctx.Err() != nil:
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
	ended, err := a.cfg.DB.EndBackgroundCall(call.ID, status, stored, failure, time.Now())
	if err != nil {
		slog.Error("background call not ended", "thread", call.ThreadID, "background", call.ID, "err", err)
		return
	}
	slog.Info("background call ended", "thread", call.ThreadID, "background", call.ID, "status", status, "recorded", ended)
	if ended && status != repo.BackgroundInterrupted {
		a.post(func() {
			a.deliverBackground(call.ThreadID)
			a.reconcile(call.ThreadID)
		})
	}
}

// deliverBackground brings the results of a thread's ended background calls back to it, into its current turn. A
// conversation with none takes them in a turn they start, on the model its last turn ran on; a subagent's thread
// that has reported takes none.
func (a *Agent) deliverBackground(threadID string) {
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		slog.Error("background results not delivered", "thread", threadID, "err", err)
		return
	}
	var next *repo.TurnRow
	if st.Thread.ParentThreadID == "" {
		model, err := a.conversationModel(st.Last, "")
		if err != nil {
			slog.Error("background results not delivered", "thread", threadID, "err", err)
			return
		}
		window, err := a.windowFor(model)
		if err != nil {
			slog.Error("background results not delivered", "thread", threadID, "err", err)
			return
		}
		next = &repo.TurnRow{ID: uuid.NewString(), Model: model.Clone(), Agent: string(role.ForMain(window.InputTokens()).Name)}
	}
	thread, turn, items, started, err := a.cfg.DB.DeliverBackgroundCalls(threadID, next)
	if err != nil {
		slog.Error("background results not delivered", "thread", threadID, "err", err)
		return
	}
	if len(items) == 0 {
		return
	}
	if started {
		a.startedTurn(thread, turn)
	}
	for _, it := range items {
		a.emitItem(thread, turn, it, true)
	}
	a.emitStatus(threadID)
	slog.Info("background results delivered", "thread", threadID, "turn", turn.ID, "results", len(items), "started", started)
}

func (a *Agent) forgetBackground(id string) {
	a.backgroundMu.Lock()
	cancel := a.backgroundCancels[id]
	delete(a.backgroundCancels, id)
	a.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelBackground ends a running call as cancelled before it stops it, so that the result its tool may still return
// does not take the cancellation's place. It reports false for a call that had already ended.
func (a *Agent) cancelBackground(id, why string) (bool, error) {
	ended, err := a.cfg.DB.EndBackgroundCall(id, repo.BackgroundCancelled, nil, why, time.Now())
	if err != nil || !ended {
		return false, err
	}
	a.forgetBackground(id)
	return true, nil
}

// cancelBackgroundOf cancels the calls still running in a thread and its subagents' threads, as one that is archived
// or deleted needs none of them.
func (a *Agent) cancelBackgroundOf(threadID, why string) error {
	ids, err := a.cfg.DB.RunningBackgroundCalls(threadID)
	if err != nil {
		return fmt.Errorf("agent: background calls of thread %s: %w", threadID, err)
	}
	for _, id := range ids {
		if _, err := a.cancelBackground(id, why); err != nil {
			return fmt.Errorf("agent: cancel background call %s: %w", id, err)
		}
	}
	return nil
}

// ToolWaitForBackground waits for background calls of the conversation to end.
const ToolWaitForBackground = "wait_for_background"

type waitForBackground struct {
	Background []string `json:"background" jsonschema:"The ids of the background calls to wait for, as their starts returned them."`
}

func (waitForBackground) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinItems = jsonschema.Ptr(1)
	s.Properties["background"].Items.MinLength = jsonschema.Ptr(1)
}

// fulfil completes a call that waits for background calls once they have all ended, with how each ended; their
// results come back to the turn as backgroundResult items. It reports whether it did.
func (a *Agent) fulfil(t repo.ThreadRow, turn repo.TurnRow, call repo.ItemRow) (bool, error) {
	var req waitForBackground
	if err := json.Unmarshal(call.Await.Request, &req); err != nil {
		return false, fmt.Errorf("agent: wait %s: %w", call.ID, err)
	}
	ended := map[string]string{}
	for _, id := range req.Background {
		bc, err := a.cfg.DB.BackgroundCall(id)
		if err != nil {
			return false, fmt.Errorf("agent: background call %s: %w", id, err)
		}
		if bc.Status == repo.BackgroundRunning {
			return false, nil
		}
		ended[id] = string(bc.Status)
	}
	result, _, err := repo.ResultOf(tools.Result{Output: map[string]any{"ended": ended}})
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	call.Status, call.Result, call.CompletedAt = repo.ItemCompleted, result, &now
	stored, _, err := a.cfg.DB.RecordItem(t.ID, call)
	if err != nil {
		return false, err
	}
	a.emitItem(t, turn, stored, false)
	return true, nil
}

// backgroundTools are the tools a conversation keeps its background calls with.
type backgroundTools struct{ a *Agent }

func (backgroundTools) Name() (string, string) {
	return "background", "Keeps the background calls of this conversation."
}

type cancelBackground struct {
	Background string `json:"background" jsonschema:"The id of the background call, as its start returned it."`
}

func (cancelBackground) Constrain(s *jsonschema.Schema) {
	s.Properties["background"].MinLength = jsonschema.Ptr(1)
}

func (backgroundTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: ToolWaitForBackground,
		Description: "Wait until background calls of this conversation have ended, when you need their results before going on. " +
			"The turn waits without holding anything; it goes on once every call named has ended, with their results.",
		InputSchema: tools.InputSchema[waitForBackground](),
		Annotations: &tools.Annotations{Title: "Wait for background calls", ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
	}, {
		Name:        ToolCancelBackground,
		Description: "Stop a background call of this conversation that is still running. Its result does not come back.",
		InputSchema: tools.InputSchema[cancelBackground](),
		Annotations: &tools.Annotations{Title: "Cancel background call", DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)},
	}}, nil
}

func (b backgroundTools) Invoke(ctx context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	if tool == ToolWaitForBackground {
		return b.wait(ctx, arguments)
	}
	args, err := tools.DecodeArguments[cancelBackground](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	call, err := b.a.cfg.DB.BackgroundCall(args.Background)
	if errors.Is(err, repo.ErrNotFound) || err == nil && call.ThreadID != tools.CallerOf(ctx).ThreadID {
		return tools.Result{}, fmt.Errorf("this conversation has no background call %q", args.Background)
	}
	if err != nil {
		return tools.Result{}, err
	}
	cancelled, err := b.a.cancelBackground(call.ID, "the agent cancelled the call")
	if err != nil {
		return tools.Result{}, err
	}
	if !cancelled {
		ended, _ := b.a.cfg.DB.BackgroundCall(call.ID)
		return tools.Result{}, fmt.Errorf("background call %s already ended: %s", call.ID, ended.Status)
	}
	return tools.Result{Output: map[string]any{"background": call.ID, "status": string(repo.BackgroundCancelled)}}, nil
}

func (b backgroundTools) wait(ctx context.Context, arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[waitForBackground](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	for _, id := range args.Background {
		call, err := b.a.cfg.DB.BackgroundCall(id)
		if errors.Is(err, repo.ErrNotFound) || err == nil && call.ThreadID != tools.CallerOf(ctx).ThreadID {
			return tools.Result{}, fmt.Errorf("this conversation has no background call %q", id)
		}
		if err != nil {
			return tools.Result{}, err
		}
	}
	return tools.Awaiting(tools.AwaitBackground, args)
}

// waitBeforeReport keeps a subagent from reporting while background calls of its own still run: it waits for them in
// the subagent's place, as a wait_for_background call of its own response, so that the subagent goes on with their
// results and reports with them. It reports whether the turn waits.
func (a *Agent) waitBeforeReport(t repo.ThreadRow, turn repo.TurnRow) (bool, error) {
	running, err := a.cfg.DB.RunningBackgroundCalls(t.ID)
	if err != nil || len(running) == 0 {
		return false, err
	}
	request, err := json.Marshal(waitForBackground{Background: running})
	if err != nil {
		return false, err
	}
	call := repo.ItemRow{
		ID: uuid.NewString(), TurnID: turn.ID, Type: repo.ItemToolCall, Status: repo.ItemInProgress,
		CallID: "wait_" + uuid.NewString(), ResponseID: uuid.NewString(), Tool: ToolWaitForBackground, Arguments: string(request),
		Await: &tools.Await{By: tools.AwaitBackground, Request: request}, StartedAt: time.Now().UTC(),
	}
	stored, _, err := a.cfg.DB.RecordItem(t.ID, call)
	if err != nil {
		return false, err
	}
	a.emitItem(t, turn, stored, true)
	slog.Info("subagent waits for its background calls before it reports", "thread", t.ID, "turn", turn.ID, "calls", len(running))
	return true, nil
}

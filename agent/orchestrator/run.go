package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"go.opentelemetry.io/otel/trace"
)

func (o *Orchestrator) run(threadID string, sl *slot) {
	if err := sl.ctx.Err(); err != nil {
		o.post(func() { o.afterRun(threadID, sl, false, err) })
		return
	}
	turn, err := o.cfg.DB.Turn(sl.turnID)
	if errors.Is(err, db.ErrNotFound) || err == nil && turn.Status != db.TurnInProgress {
		o.post(func() { o.release(threadID) })
		return
	}
	var t db.ThreadRow
	if err == nil {
		t, err = o.cfg.DB.ThreadRow(threadID)
	}
	if err != nil {
		slog.Error("thread not readable to run", "thread", threadID, "turn", sl.turnID, "err", err)
		o.post(func() {
			o.release(threadID)
			o.failTurn(sl.turnID, db.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	// The run joins the turn's trace, and holds its slot no longer than the time limit.
	limit := fmt.Errorf("orchestrator: the turn reached its time limit of %s", o.cfg.RunLimit)
	ctx, cancel := context.WithTimeoutCause(trace.ContextWithRemoteSpanContext(sl.ctx, turnTrace(turn.ID)), o.cfg.RunLimit, limit)
	defer cancel()
	started := time.Now()
	final, usage, err := o.runTurn(ctx, t, turn)
	if err != nil && context.Cause(ctx) == limit {
		err = limit
	}
	slog.InfoContext(ctx, "run ended", "thread", threadID, "turn", turn.ID, "ms", time.Since(started).Milliseconds(), "final", final)
	if err := o.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		slog.Error("turn usage not recorded", "thread", threadID, "turn", turn.ID, "err", err)
	}
	o.post(func() { o.afterRun(threadID, sl, final, err) })
}

// runTurn runs the turn on what its user runs it with: the model client of its connection, the model's context window
// and the user's tools. The runtime reads the rest itself.
func (o *Orchestrator) runTurn(ctx context.Context, t db.ThreadRow, turn db.TurnRow) (bool, db.Usage, error) {
	window, err := o.windowFor(turn.Model)
	if err != nil {
		return false, db.Usage{}, err
	}
	client, err := o.clientFor(t.UserID, turn.Model.Connection)
	if err != nil {
		return false, db.Usage{}, fmt.Errorf("orchestrator: the model client of the user: %w", err)
	}
	registry, err := o.registryFor(t.UserID)
	if err != nil {
		return false, db.Usage{}, err
	}
	rt := &runtime.Runtime{
		DB: o.cfg.DB, Attachments: o.cfg.Attachments, Memory: o.cfg.Memory,
		LLM: client, Registry: registry, Window: window,
		Host: turnHost{o: o, t: t, turn: turn, registry: registry},
		Config: runtime.Config{
			Name: o.cfg.Name, Prompt: o.cfg.Prompt, SkillsDir: o.cfg.SkillsDir,
			Settings: o.cfg.Settings, Shared: o.cfg.ToolsFor != nil, Tools: o.cfg.Tools,
		},
		TurnID: turn.ID,
	}
	return rt.Run(ctx)
}

func (o *Orchestrator) afterRun(threadID string, sl *slot, final bool, runErr error) {
	if o.slotByThread[threadID] != sl {
		return
	}
	interrupted := sl.ctx.Err() != nil
	o.release(threadID)
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		slog.Error("thread not readable after its run", "thread", threadID, "turn", sl.turnID, "err", err)
		if interrupted {
			o.failTurn(sl.turnID, db.TurnInterrupted, "interrupted by the user")
		} else {
			o.failTurn(sl.turnID, db.TurnFailed, "the thread could not be read from storage after its run")
		}
		return
	}
	turn, ok := st.ActiveTurn()
	if !ok || turn.ID != sl.turnID {
		return
	}
	switch {
	case o.ctx.Err() != nil && (runErr != nil || interrupted):
		slog.Info("turn left in progress at shutdown", "thread", threadID, "turn", turn.ID, "err", runErr)
		return
	case interrupted:
		turn.Status, turn.Error = db.TurnInterrupted, "interrupted by the user"
	case runErr != nil:
		turn.Status, turn.Error = db.TurnFailed, runErr.Error()
		slog.Warn("turn failed", "thread", threadID, "turn", turn.ID, "err", runErr)
	case final:
		more, err := o.cfg.DB.InputAfterAnswer(turn.ID)
		if err != nil {
			o.halt(threadID, err)
			return
		}
		if !more && st.Thread.ParentThreadID != "" {
			if more, err = o.waitBeforeReport(st.Thread, turn); err != nil {
				o.halt(threadID, err)
				return
			}
		}
		if !more {
			turn.Status = db.TurnCompleted
		}
	}
	if turn.Status != db.TurnInProgress {
		o.endTurn(turn)
		return
	}
	if _, err := o.cfg.DB.Park(turn.ID); err != nil {
		o.halt(threadID, err)
		return
	}
	o.reconcile(threadID)
	if n := len(st.PendingRequests()); n > 0 {
		slog.Info("turn waiting", "thread", threadID, "turn", turn.ID, "on", "user", "requests", n)
		o.emitRequests(st)
		o.emitStatus(threadID)
	} else if n := len(st.AgentCalls()); n > 0 {
		slog.Info("turn waiting", "thread", threadID, "turn", turn.ID, "on", "subagents", "subagents", n)
	}
}

func (o *Orchestrator) endTurn(turn db.TurnRow) {
	st, err := o.cfg.DB.ThreadState(turn.ThreadID)
	if err != nil {
		o.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn could not be ended: %w", err))
		return
	}
	thread, ended, closed, err := o.cfg.DB.EndTurn(turn.ThreadID, turn)
	if err != nil {
		o.fault(turn.ThreadID, turn.ID, fmt.Errorf("the end of the turn could not be recorded: %w", err))
		return
	}
	for _, it := range closed {
		o.emitItem(thread, ended, it, false)
	}
	attrs := []any{"thread", thread.ID, "turn", ended.ID, "status", ended.Status, "input", ended.Usage.InputTokens, "output", ended.Usage.OutputTokens}
	if ended.CompletedAt != nil {
		attrs = append(attrs, "ms", ended.CompletedAt.Sub(ended.StartedAt).Milliseconds())
	}
	slog.Info("turn ended", attrs...)
	o.emit(Event{Kind: EventTurnCompleted, Thread: thread, Turn: ended})
	o.emitStatus(turn.ThreadID)
	for _, call := range st.AgentCalls() {
		o.stopSubagent(call.Await.Handle)
	}
	o.reconcile(turn.ThreadID)
	if st.Thread.ParentThreadID != "" {
		o.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (o *Orchestrator) stopSubagent(threadID string) {
	if err := o.cancelBackgroundOf(threadID, "the subagent was stopped"); err != nil {
		o.fault(threadID, "", err)
	}
	if sl, ok := o.slotByThread[threadID]; ok {
		sl.cancel()
		return
	}
	turn, err := o.cfg.DB.ActiveTurn(threadID)
	if errors.Is(err, db.ErrNotFound) {
		// A subagent whose turn is still queued never started: its turn starts, to end as stopped.
		var st db.ThreadState
		if st, err = o.cfg.DB.ThreadState(threadID); err == nil {
			if len(st.QueuedTurns()) == 0 {
				return
			}
			_, turn, _, err = o.cfg.DB.StartQueuedTurn(threadID)
		}
	}
	if err != nil {
		o.fault(threadID, "", fmt.Errorf("the subagent could not be stopped: %w", err))
		return
	}
	turn.Status = db.TurnInterrupted
	turn.Error = "stopped with the turn that launched it"
	o.endTurn(turn)
}

func (o *Orchestrator) halt(threadID string, cause error) {
	slog.Error("thread cannot advance", "thread", threadID, "err", cause)
	turn, err := o.cfg.DB.ActiveTurn(threadID)
	if err != nil {
		o.fault(threadID, "", fmt.Errorf("the thread could not advance: %w", cause))
		return
	}
	turn.Status, turn.Error = db.TurnFailed, "the agent could not advance this turn: "+cause.Error()
	o.endTurn(turn)
}

func (o *Orchestrator) failTurn(turnID string, status db.TurnStatus, reason string) {
	turn, err := o.cfg.DB.Turn(turnID)
	if err != nil {
		o.fault("", turnID, fmt.Errorf("the turn could not be read to end it (%s): %w", reason, err))
		return
	}
	if turn.Status.Ended() {
		return
	}
	turn.Status, turn.Error = status, reason
	o.endTurn(turn)
}

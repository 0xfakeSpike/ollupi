package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/agent/systemprompt"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func (a *Agent) run(threadID string, sl *slot) {
	if err := sl.ctx.Err(); err != nil {
		a.post(func() { a.afterRun(threadID, sl, false, err) })
		return
	}
	turn, err := a.cfg.DB.Turn(sl.turnID)
	if errors.Is(err, repo.ErrNotFound) || err == nil && turn.Status != repo.TurnInProgress {
		a.post(func() { a.release(threadID) })
		return
	}
	var t repo.ThreadRow
	var items []repo.ItemRow
	if err == nil {
		t, err = a.cfg.DB.ThreadRow(threadID)
	}
	if err == nil {
		items, err = a.cfg.DB.RunItems(threadID)
	}
	if err != nil {
		slog.Error("thread not readable to run", "thread", threadID, "turn", sl.turnID, "err", err)
		a.post(func() {
			a.release(threadID)
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	started := time.Now()
	final, usage, err := a.runTurn(sl.ctx, t, turn, items)
	slog.Info("run ended", "thread", threadID, "turn", turn.ID, "ms", time.Since(started).Milliseconds(), "final", final, "items", len(items))
	if err := a.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		slog.Error("turn usage not recorded", "thread", threadID, "turn", turn.ID, "err", err)
	}
	a.post(func() { a.afterRun(threadID, sl, final, err) })
}

func (a *Agent) runTurn(ctx context.Context, t repo.ThreadRow, turn repo.TurnRow, thread []repo.ItemRow) (bool, repo.Usage, error) {
	window, err := a.windowFor(turn.Model)
	if err != nil {
		return false, repo.Usage{}, err
	}
	checker, w, err := a.permissions(t)
	if err != nil {
		return false, repo.Usage{}, err
	}
	var history, items []repo.ItemRow
	for _, it := range thread {
		if it.TurnID == turn.ID {
			items = append(items, it)
		} else {
			history = append(history, it)
		}
	}
	rt := &runtime.Runtime{
		LLM:         a.cfg.Connections[turn.Model.Connection],
		Registry:    a.registry,
		Skills:      a.skills,
		Window:      window,
		Memory:      a.cfg.Memory,
		Turn:        turn,
		Caller:      tools.Caller{ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), Root: w.RootPath, Subagent: t.ParentThreadID != ""},
		Prompt:      systemprompt.AgentPrompt(a.cfg.Name, a.cfg.Prompt),
		Permissions: checker,
		History:     history,
		Items:       items,
		Record:      a.recorder(t, turn),
		Delta: func(it repo.ItemRow, text string) {
			a.emit(Event{Kind: EventItemDelta, Thread: t, Turn: turn, Item: &it, Delta: text})
		},
		Reload: func() ([]repo.ItemRow, *permission.Checker, error) {
			items, err := a.cfg.DB.TurnItems(turn.ID)
			if err != nil {
				return nil, nil, err
			}
			current, err := a.cfg.DB.ThreadRow(t.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("agent: thread %s: %w", t.ID, err)
			}
			checker, _, err := a.permissions(current)
			return items, checker, err
		},
		AttachmentsDir: a.cfg.AttachmentsDir,
		Settings:       a.cfg.DB.Settings,
	}
	return rt.Run(ctx)
}

func (a *Agent) recorder(t repo.ThreadRow, turn repo.TurnRow) func(repo.ItemRow) (repo.ItemRow, error) {
	return func(it repo.ItemRow) (repo.ItemRow, error) {
		stored, isNew, err := a.cfg.DB.RecordItem(t.ID, it)
		if err != nil {
			return repo.ItemRow{}, err
		}
		a.emitItem(t, turn, stored, isNew)
		return stored, nil
	}
}

func (a *Agent) afterRun(threadID string, sl *slot, final bool, runErr error) {
	if a.slotByThread[threadID] != sl {
		return
	}
	interrupted := sl.ctx.Err() != nil
	a.release(threadID)
	st, err := a.cfg.DB.ThreadState(threadID)
	if err != nil {
		slog.Error("thread not readable after its run", "thread", threadID, "turn", sl.turnID, "err", err)
		if interrupted {
			a.failTurn(sl.turnID, repo.TurnInterrupted, "interrupted by the user")
		} else {
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage after its run")
		}
		return
	}
	turn, ok := st.ActiveTurn()
	if !ok || turn.ID != sl.turnID {
		return
	}
	switch {
	case a.ctx.Err() != nil && (runErr != nil || interrupted):
		slog.Info("turn left in progress at shutdown", "thread", threadID, "turn", turn.ID, "err", runErr)
		return
	case interrupted:
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted by the user"
	case runErr != nil:
		turn.Status, turn.Error = repo.TurnFailed, runErr.Error()
		slog.Warn("turn failed", "thread", threadID, "turn", turn.ID, "err", runErr)
	case final:
		more, err := a.cfg.DB.InputAfterAnswer(turn.ID)
		if err != nil {
			a.halt(threadID, err)
			return
		}
		if !more {
			turn.Status = repo.TurnCompleted
		}
	}
	if turn.Status != repo.TurnInProgress {
		a.endTurn(turn)
		return
	}
	a.reconcile(threadID)
	if n := len(st.PendingRequests()); n > 0 {
		slog.Info("turn waiting", "thread", threadID, "turn", turn.ID, "on", "user", "requests", n)
		a.emitRequests(st)
		a.emitStatus(threadID)
	} else if n := len(st.AgentCalls()); n > 0 {
		slog.Info("turn waiting", "thread", threadID, "turn", turn.ID, "on", "subagents", "subagents", n)
	}
}

func (a *Agent) endTurn(turn repo.TurnRow) {
	st, err := a.cfg.DB.ThreadState(turn.ThreadID)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the turn could not be ended: %w", err))
		return
	}
	thread, ended, closed, err := a.cfg.DB.EndTurn(turn.ThreadID, turn)
	if err != nil {
		a.fault(turn.ThreadID, turn.ID, fmt.Errorf("the end of the turn could not be recorded: %w", err))
		return
	}
	for _, it := range closed {
		a.emitItem(thread, ended, it, false)
	}
	attrs := []any{"thread", thread.ID, "turn", ended.ID, "status", ended.Status, "input", ended.Usage.InputTokens, "output", ended.Usage.OutputTokens}
	if ended.CompletedAt != nil {
		attrs = append(attrs, "ms", ended.CompletedAt.Sub(ended.StartedAt).Milliseconds())
	}
	slog.Info("turn ended", attrs...)
	a.emit(Event{Kind: EventTurnCompleted, Thread: thread, Turn: ended})
	a.emitStatus(turn.ThreadID)
	for _, call := range st.AgentCalls() {
		a.stopSubagent(call.Await.ThreadID)
	}
	a.reconcile(turn.ThreadID)
	if st.Thread.ParentThreadID != "" {
		a.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (a *Agent) stopSubagent(threadID string) {
	if sl, ok := a.slotByThread[threadID]; ok {
		sl.cancel()
		return
	}
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if errors.Is(err, repo.ErrNotFound) {
		return
	}
	if err != nil {
		a.fault(threadID, "", fmt.Errorf("the subagent could not be stopped: %w", err))
		return
	}
	turn.Status = repo.TurnInterrupted
	turn.Error = "stopped with the turn that launched it"
	a.endTurn(turn)
}

func (a *Agent) halt(threadID string, cause error) {
	slog.Error("thread cannot advance", "thread", threadID, "err", cause)
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if err != nil {
		a.fault(threadID, "", fmt.Errorf("the thread could not advance: %w", cause))
		return
	}
	turn.Status, turn.Error = repo.TurnFailed, "the agent could not advance this turn: "+cause.Error()
	a.endTurn(turn)
}

func (a *Agent) failTurn(turnID string, status repo.TurnStatus, reason string) {
	turn, err := a.cfg.DB.Turn(turnID)
	if err != nil {
		a.fault("", turnID, fmt.Errorf("the turn could not be read to end it (%s): %w", reason, err))
		return
	}
	if turn.Status != repo.TurnInProgress {
		return
	}
	turn.Status, turn.Error = status, reason
	a.endTurn(turn)
}

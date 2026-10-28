package agent

import (
	"context"
	"fmt"
	"log"

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
	view, err := a.cfg.DB.Thread(threadID)
	if err != nil {
		log.Printf("agent: thread %s cannot be read while turn %s is queued: %v", threadID, sl.turnID, err)
		a.post(func() {
			a.release(threadID)
			a.failTurn(sl.turnID, repo.TurnFailed, "the thread could not be read from storage")
		})
		return
	}
	turn, ok := findTurn(view, sl.turnID)
	if !ok || turn.Status != repo.TurnInProgress {
		a.post(func() { a.release(threadID) })
		return
	}
	final, usage, err := a.runTurn(sl.ctx, view, turn)
	if err := a.cfg.DB.AddUsage(threadID, turn.ID, usage); err != nil {
		log.Printf("agent: thread %s: record usage of turn %s: %v", threadID, turn.ID, err)
	}
	a.post(func() { a.afterRun(threadID, sl, final, err) })
}

func findTurn(view repo.ThreadView, turnID string) (repo.TurnRow, bool) {
	for _, t := range view.Turns {
		if t.ID == turnID {
			return t, true
		}
	}
	return repo.TurnRow{}, false
}

func (a *Agent) runTurn(ctx context.Context, view repo.ThreadView, turn repo.TurnRow) (bool, repo.Usage, error) {
	contexts, err := a.contextsFor(turn.Model)
	if err != nil {
		return false, repo.Usage{}, err
	}
	checker, w, err := a.permissions(view.Thread)
	if err != nil {
		return false, repo.Usage{}, err
	}
	var history, items []repo.ItemRow
	for _, it := range view.Items {
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
		Contexts:    contexts,
		Memory:      a.cfg.Memory,
		Turn:        turn,
		Caller:      tools.Caller{ThreadID: view.Thread.ID, WorkspaceID: string(view.Thread.WorkspaceID), Root: w.RootPath, Subagent: view.Thread.ParentThreadID != ""},
		Prompt:      systemprompt.AgentPrompt(a.cfg.Name, a.cfg.Prompt),
		Tools:       checker.Tools(),
		Permissions: checker,
		History:     history,
		Items:       items,
		Record:      a.recorder(view.Thread, turn),
		Delta: func(it repo.ItemRow, text string) {
			a.emit(Event{Kind: EventItemDelta, Thread: view.Thread, Turn: turn, Item: &it, Delta: text})
		},
		Reload: func() ([]repo.ItemRow, *permission.Checker, error) {
			items, err := a.cfg.DB.TurnItems(view.Thread.ID, turn.ID)
			if err != nil {
				return nil, nil, err
			}
			t, err := a.cfg.DB.ThreadRow(view.Thread.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("agent: thread %s: %w", view.Thread.ID, err)
			}
			checker, _, err := a.permissions(t)
			return items, checker, err
		},
		Redaction: func() (bool, error) {
			settings, err := a.cfg.DB.Settings()
			return settings.Redaction, err
		},
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
		log.Printf("agent: thread %s cannot be read after its run: %v", threadID, err)
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
		log.Printf("agent: thread %s turn %s left in progress at shutdown: %v", threadID, turn.ID, runErr)
		return
	case interrupted:
		turn.Status, turn.Error = repo.TurnInterrupted, "interrupted by the user"
	case runErr != nil:
		turn.Status, turn.Error = repo.TurnFailed, runErr.Error()
		log.Printf("agent: thread %s turn %s failed: %v", threadID, turn.ID, runErr)
	case final:
		more, err := a.cfg.DB.InputAfterAnswer(threadID, turn.ID)
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
	if len(st.PendingRequests()) > 0 {
		a.emitRequests(st)
		a.emitStatus(threadID)
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
	a.emit(Event{Kind: EventTurnCompleted, Thread: thread, Turn: ended})
	a.emitStatus(turn.ThreadID)
	for _, call := range st.AgentCalls() {
		a.stopThread(call.Await.ThreadID)
	}
	a.reconcile(turn.ThreadID)
	if st.Thread.ParentThreadID != "" {
		a.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (a *Agent) stopThread(threadID string) {
	if sl, ok := a.slotByThread[threadID]; ok {
		sl.cancel()
		return
	}
	turn, err := a.cfg.DB.ActiveTurn(threadID)
	if repo.IsNotFound(err) {
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
	log.Printf("agent: thread %s cannot advance: %v", threadID, cause)
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

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// deliverBackground brings the results of a thread's ended background calls back to it, into its current turn. A
// conversation with none takes them in a turn they start, on the model its last turn ran on; a subagent's thread
// that has reported takes none.
func (o *Orchestrator) deliverBackground(threadID string) {
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		slog.Error("background results not delivered", "thread", threadID, "err", err)
		return
	}
	var next *repo.TurnRow
	if st.Thread.ParentThreadID == "" {
		model, err := o.conversationModel(st.Last, "")
		if err != nil {
			slog.Error("background results not delivered", "thread", threadID, "err", err)
			return
		}
		main, err := o.mainRole(model)
		if err != nil {
			slog.Error("background results not delivered", "thread", threadID, "err", err)
			return
		}
		next = &repo.TurnRow{ID: uuid.NewString(), Model: model.Clone(), Agent: string(main.Name)}
	}
	thread, turn, items, started, err := o.cfg.DB.DeliverBackgroundCalls(threadID, next)
	if err != nil {
		slog.Error("background results not delivered", "thread", threadID, "err", err)
		return
	}
	if len(items) == 0 {
		return
	}
	if started {
		o.startedTurn(thread, turn)
	}
	for _, it := range items {
		o.emitItem(thread, turn, it, true)
	}
	o.emitStatus(threadID)
	slog.Info("background results delivered", "thread", threadID, "turn", turn.ID, "results", len(items), "started", started)
}

// fulfil completes a call that waits for background calls once they have all ended, with how each ended; their
// results come back to the turn as backgroundResult items. It reports whether it did.
func (o *Orchestrator) fulfil(t repo.ThreadRow, turn repo.TurnRow, call repo.ItemRow) (bool, error) {
	var req waitForBackground
	if err := json.Unmarshal(call.Await.Request, &req); err != nil {
		return false, fmt.Errorf("orchestrator: wait %s: %w", call.ID, err)
	}
	ended := map[string]string{}
	for _, id := range req.Background {
		bc, err := o.cfg.DB.BackgroundCall(id)
		if err != nil {
			return false, fmt.Errorf("orchestrator: background call %s: %w", id, err)
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
	stored, _, err := o.cfg.DB.RecordItem(t.ID, call)
	if err != nil {
		return false, err
	}
	o.emitItem(t, turn, stored, false)
	return true, nil
}

// waitBeforeReport keeps a subagent from reporting while background calls of its own still run: it waits for them in
// the subagent's place, as a wait_for_background call of its own response, so that the subagent goes on with their
// results and reports with them. It reports whether the turn waits.
func (o *Orchestrator) waitBeforeReport(t repo.ThreadRow, turn repo.TurnRow) (bool, error) {
	running, err := o.cfg.DB.RunningBackgroundCalls(t.ID)
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
	stored, _, err := o.cfg.DB.RecordItem(t.ID, call)
	if err != nil {
		return false, err
	}
	o.emitItem(t, turn, stored, true)
	slog.Info("subagent waits for its background calls before it reports", "thread", t.ID, "turn", turn.ID, "calls", len(running))
	return true, nil
}

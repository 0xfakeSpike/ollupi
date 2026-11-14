package orchestrator

import (
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/background"
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
	var next *db.TurnRow
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
		next = &db.TurnRow{ID: uuid.NewString(), Model: model.Clone(), Agent: string(main.Name)}
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

// fulfil completes a call that waits for background calls once they have all ended, and tells the thread. It reports
// whether it did.
func (o *Orchestrator) fulfil(t db.ThreadRow, turn db.TurnRow, call db.ItemRow) (bool, error) {
	done, ok, err := background.Fulfilled(o.cfg.DB, call)
	if err != nil || !ok {
		return false, err
	}
	stored, _, err := o.cfg.DB.RecordItem(t.ID, done)
	if err != nil {
		return false, err
	}
	o.emitItem(t, turn, stored, false)
	return true, nil
}

// waitBeforeReport keeps a subagent from reporting while background calls of its own still run, by a wait for them in
// its place. It reports whether the turn waits.
func (o *Orchestrator) waitBeforeReport(t db.ThreadRow, turn db.TurnRow) (bool, error) {
	call, ok, err := background.WaitInPlace(o.cfg.DB, t.ID, turn.ID)
	if err != nil || !ok {
		return false, err
	}
	stored, _, err := o.cfg.DB.RecordItem(t.ID, call)
	if err != nil {
		return false, err
	}
	o.emitItem(t, turn, stored, true)
	slog.Info("subagent waits for its background calls before it reports", "thread", t.ID, "turn", turn.ID, "call", stored.ID)
	return true, nil
}

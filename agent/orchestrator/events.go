package orchestrator

import (
	"github.com/0xfakeSpike/ollupi/agent/event"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/repo"
)

func (o *Orchestrator) fault(threadID, turnID string, err error) {
	slog.Error("orchestrator fault", "thread", threadID, "turn", turnID, "err", err)
	t, rerr := o.cfg.DB.ThreadRow(threadID)
	if rerr != nil {
		t = repo.ThreadRow{ID: threadID}
	}
	o.emit(event.Event{Kind: event.Error, Thread: t, Turn: repo.TurnRow{ID: turnID, ThreadID: threadID}, Error: err.Error()})
}

func (o *Orchestrator) emit(e event.Event) {
	if o.cfg.OnEvent != nil {
		o.cfg.OnEvent(e)
	}
}

func (o *Orchestrator) emitStatus(threadID string) {
	t, err := o.cfg.DB.ThreadRow(threadID)
	if err != nil {
		slog.Warn("thread status not sent", "thread", threadID, "err", err)
		return
	}
	if t.ParentThreadID != "" {
		o.emitStatus(string(t.ParentThreadID))
		return
	}
	status := o.threadStatus(threadID)
	o.emit(event.Event{Kind: event.ThreadStatusChanged, Thread: t, Status: status})
}

func (o *Orchestrator) emitRequests(st repo.ThreadState) {
	turn, _ := st.ActiveTurn()
	for _, it := range st.PendingRequests() {
		kind := event.ApprovalRequested
		if !it.WaitsForApproval() {
			kind = event.UserInputRequested
		}
		o.emit(event.Event{Kind: kind, Thread: st.Thread, Turn: turn, Item: &it})
	}
}

func (o *Orchestrator) emitItem(t repo.ThreadRow, turn repo.TurnRow, it repo.ItemRow, isNew bool) {
	for _, e := range event.OfItem(t, turn, it, isNew) {
		o.emit(e)
	}
}

// startedTurn logs a turn that starts and tells the clients.
func (o *Orchestrator) startedTurn(t repo.ThreadRow, turn repo.TurnRow) {
	slog.Info("turn started", "thread", t.ID, "turn", turn.ID, "model", turn.Model.Name, "connection", turn.Model.Connection,
		"subagent", t.ParentThreadID != "", "cronJob", turn.CronJobID)
	o.emit(event.Event{Kind: event.TurnStarted, Thread: t, Turn: turn})
}

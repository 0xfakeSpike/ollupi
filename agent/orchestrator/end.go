package orchestrator

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) endTurn(turn db.TurnRow) {
	st, err := o.store.ThreadState(turn.ThreadID)
	if err != nil {
		o.fault(turn.ThreadID, turn.ID, "the turn could not be ended")
		return
	}
	thread, ended, closed, report, err := o.store.EndTurn(turn.ThreadID, turn)
	if err != nil {
		o.fault(turn.ThreadID, turn.ID, "the end of the turn could not be recorded")
		return
	}
	o.ended(st, thread, ended, closed, report)
}

func (o *Orchestrator) ended(st db.ThreadState, thread db.ThreadRow, ended db.TurnRow, closed []db.ItemRow, report *db.SubagentReport) {
	for _, it := range closed {
		o.ev.Item(thread, ended, it, false)
	}
	if report != nil {
		o.ev.Item(report.Thread, report.Turn, report.Item, true)
		o.status(report.Thread.ID)
	}
	o.ev.Publish(event.Event{Kind: event.TurnCompleted, User: thread.UserID, Thread: thread, Turn: ended})
	if behind := st.Behind(); len(behind) > 0 {
		o.ev.Publish(event.Event{Kind: event.TurnStarted, User: thread.UserID, Thread: thread, Turn: behind[0]})
	}
	o.status(thread.ID)
	// A turn ends only once its subagents did, but one that fails or is stopped takes those still running with it.
	running, err := o.store.RunningSubagents(ended.ID)
	if err != nil {
		o.fault(thread.ID, ended.ID, "the subagents of the turn could not be read to stop them")
	}
	for _, child := range running {
		o.stopSubagent(child)
	}
	o.reconcile(thread.ID)
	if st.Thread.ParentThreadID != "" {
		o.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (o *Orchestrator) halt(threadID string) {
	turn, err := o.store.ActiveTurn(threadID)
	if err != nil {
		o.fault(threadID, "", "the thread could not advance")
		return
	}
	turn.Status, turn.Error = db.TurnFailed, "the agent could not advance this turn"
	o.endTurn(turn)
}

func (o *Orchestrator) failTurn(turnID string, status db.TurnStatus, reason string) {
	turn, err := o.store.Turn(turnID)
	if err != nil {
		o.fault("", turnID, fmt.Sprintf("the turn could not be read to end it (%s)", reason))
		return
	}
	if turn.Status.Ended() {
		return
	}
	turn.Status, turn.Error = status, reason
	o.endTurn(turn)
}

func (o *Orchestrator) status(threadID string) {
	if t, status, err := o.store.TopThreadStatus(threadID); err == nil {
		o.ev.Status(t, status)
	}
}

// What, not why: the cause is the storage's own words, with its paths and tables.
func (o *Orchestrator) fault(threadID, turnID, what string) {
	t, err := o.store.ThreadRow(threadID)
	if err != nil {
		t = db.ThreadRow{ID: threadID}
	}
	o.ev.Publish(event.Event{Kind: event.Error, User: t.UserID, Thread: t, Turn: db.TurnRow{ID: turnID, ThreadID: threadID}, Error: what})
}

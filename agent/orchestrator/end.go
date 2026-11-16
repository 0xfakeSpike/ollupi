package orchestrator

import (
	"fmt"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) endTurn(turn db.TurnRow) {
	st, err := o.cfg.DB.ThreadState(turn.ThreadID)
	if err != nil {
		o.ev.Fault(turn.ThreadID, turn.ID, "the turn could not be ended", err)
		return
	}
	thread, ended, closed, report, err := o.cfg.DB.EndTurn(turn.ThreadID, turn)
	if err != nil {
		o.ev.Fault(turn.ThreadID, turn.ID, "the end of the turn could not be recorded", err)
		return
	}
	o.ended(st, thread, ended, closed, report)
}

// ended tells of the turn that ended, with the conversation as st read it before, and moves on what waited on it: the
// turn behind it, the subagents it leaves running, and the turn that launched it.
func (o *Orchestrator) ended(st db.ThreadState, thread db.ThreadRow, ended db.TurnRow, closed []db.ItemRow, report *db.SubagentReport) {
	for _, it := range closed {
		o.ev.Item(thread, ended, it, false)
	}
	if report != nil {
		o.ev.Item(report.Thread, report.Turn, report.Item, true)
		o.ev.Status(report.Thread.ID)
	}
	slog.Info("turn ended", "thread", thread.ID, "turn", ended.ID, "status", ended.Status, "input", ended.Usage.InputTokens,
		"output", ended.Usage.OutputTokens, "ms", ended.CompletedAt.Sub(ended.StartedAt).Milliseconds())
	o.ev.Emit(event.Event{Kind: event.TurnCompleted, Thread: thread, Turn: ended})
	// The first of the turns behind it is the thread's current turn now.
	if behind := st.Behind(); len(behind) > 0 {
		o.ev.TurnStarted(thread, behind[0])
	}
	o.ev.Status(thread.ID)
	// A turn ends only once its subagents did, but one that fails or is stopped takes those still running with it.
	running, err := o.cfg.DB.RunningSubagents(ended.ID)
	if err != nil {
		o.ev.Fault(thread.ID, ended.ID, "the subagents of the turn could not be read to stop them", err)
	}
	for _, child := range running {
		o.stopSubagent(child)
	}
	o.reconcile(thread.ID)
	if st.Thread.ParentThreadID != "" {
		o.reconcile(string(st.Thread.ParentThreadID))
	}
}

func (o *Orchestrator) halt(threadID string, cause error) {
	slog.Error("thread cannot advance", "thread", threadID, "err", cause)
	turn, err := o.cfg.DB.ActiveTurn(threadID)
	if err != nil {
		o.ev.Fault(threadID, "", "the thread could not advance", cause)
		return
	}
	turn.Status, turn.Error = db.TurnFailed, "the agent could not advance this turn"
	o.endTurn(turn)
}

func (o *Orchestrator) failTurn(turnID string, status db.TurnStatus, reason string) {
	turn, err := o.cfg.DB.Turn(turnID)
	if err != nil {
		o.ev.Fault("", turnID, fmt.Sprintf("the turn could not be read to end it (%s)", reason), err)
		return
	}
	if turn.Status.Ended() {
		return
	}
	turn.Status, turn.Error = status, reason
	o.endTurn(turn)
}

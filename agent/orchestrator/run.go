package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/repo/db"
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
	// The run holds its slot no longer than the time limit.
	limit := fmt.Errorf("orchestrator: the turn reached its time limit of %s", o.cfg.RunLimit)
	ctx, cancel := context.WithTimeoutCause(sl.ctx, o.cfg.RunLimit, limit)
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
// and the user's tools, as the role it names, which reads the turn, its conversation and the user's settings.
func (o *Orchestrator) runTurn(ctx context.Context, t db.ThreadRow, turn db.TurnRow) (bool, db.Usage, error) {
	window, err := o.res.Window(turn.Model)
	if err != nil {
		return false, db.Usage{}, err
	}
	client, err := o.res.Client(t.UserID, turn.Model.Connection)
	if err != nil {
		return false, db.Usage{}, fmt.Errorf("orchestrator: the model client of the user: %w", err)
	}
	registry, err := o.res.Registry(t.UserID)
	if err != nil {
		return false, db.Usage{}, err
	}
	r, ok := role.New(role.Name(turn.Agent), o.cfg.Name, o.cfg.Prompt, o.cfg.SkillsDir, o.cfg.Settings, o.cfg.Shared, o.cfg.Tools, o.cfg.Memory, window)
	if !ok {
		return false, db.Usage{}, fmt.Errorf("orchestrator: turn %s names role %q, which is none of %q", turn.ID, turn.Agent, role.NameValues)
	}
	r.LLM, r.DB, r.Attachments, r.Registry, r.Events = client, o.cfg.DB, o.cfg.Attachments, registry, o.ev
	if err := r.Load(turn.ID); err != nil {
		return false, db.Usage{}, fmt.Errorf("orchestrator: %w", err)
	}
	return r.Run(ctx)
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
		thread, settled, closed, report, err := o.cfg.DB.AfterAnswer(threadID, turn)
		switch {
		case err != nil:
			o.halt(threadID, err)
		case settled.Status == db.TurnCompleted:
			o.ended(st, thread, settled, closed, report)
		case settled.Status == db.TurnWaitingOnAgent:
			slog.Info("turn waiting", "thread", threadID, "turn", turn.ID, "on", "subagents")
			o.ev.Status(threadID)
			o.reconcile(threadID)
		default:
			o.reconcile(threadID)
		}
		return
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
		o.ev.Requests(st)
		o.ev.Status(threadID)
	}
}

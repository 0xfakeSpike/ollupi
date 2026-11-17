package orchestrator

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// A turn that ended before its run began is not in progress, and afterRun leaves it as it is.
func (o *Orchestrator) run(threadID string, sl *slot) (bool, error) {
	if err := sl.ctx.Err(); err != nil {
		return false, err
	}
	turn, err := o.store.Turn(sl.turnID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: the turn could not be read from storage: %w", err)
	}
	if turn.Status != db.TurnInProgress {
		return false, nil
	}
	t, err := o.store.ThreadRow(threadID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: the thread could not be read from storage: %w", err)
	}
	limit := fmt.Errorf("orchestrator: the turn reached its time limit of %s", o.runLimit)
	ctx, cancel := context.WithTimeoutCause(sl.ctx, o.runLimit, limit)
	defer cancel()
	final, usage, err := o.runTurn(ctx, t, turn)
	if err != nil && context.Cause(ctx) == limit {
		err = limit
	}
	o.store.AddUsage(threadID, turn.ID, usage)
	return final, err
}

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
	r, ok := role.New(role.Name(turn.Agent), o.name, o.prompt, o.skillsDir, o.permissions, o.memory, window,
		client, o.store, o.attachments, registry, o.ev)
	if !ok {
		return false, db.Usage{}, fmt.Errorf("orchestrator: turn %s names role %q, which is none of %q", turn.ID, turn.Agent, role.NameValues)
	}
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
	st, err := o.store.ThreadState(threadID)
	if err != nil {
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
		return
	case interrupted:
		turn.Status, turn.Error = db.TurnInterrupted, "interrupted by the user"
	case runErr != nil:
		turn.Status, turn.Error = db.TurnFailed, runErr.Error()
	case final:
		thread, settled, closed, report, err := o.store.AfterAnswer(threadID, turn)
		switch {
		case err != nil:
			o.halt(threadID)
		case settled.Status == db.TurnCompleted:
			o.ended(st, thread, settled, closed, report)
		case settled.Status == db.TurnWaitingOnAgent:
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
	if _, err := o.store.Park(turn.ID); err != nil {
		o.halt(threadID)
		return
	}
	o.reconcile(threadID)
	if n := len(st.PendingRequests()); n > 0 {
		o.ev.Requests(st)
		o.ev.Status(threadID)
	}
}

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) reconcile(threadID string) {
	if _, busy := o.slotByThread[threadID]; busy || o.ctx.Err() != nil {
		return
	}
	st, err := o.cfg.DB.ThreadState(threadID)
	if err != nil {
		o.halt(threadID, err)
		return
	}
	turn, active := st.ActiveTurn()
	if !active {
		return
	}
	if _, err := o.res.Model(turn.Model.Connection); err != nil {
		turn.Status, turn.Error = db.TurnFailed, err.Error()
		o.endTurn(turn)
		return
	}
	if turn.Status != db.TurnReady || len(o.slotByThread) >= o.cfg.Slots || o.slotsOfUser[st.Thread.UserID] >= o.cfg.UserSlots {
		return
	}
	if err := o.cfg.DB.Launch(turn.ID); err != nil {
		o.halt(threadID, err)
		return
	}
	o.launch(st.Thread.UserID, threadID, turn.ID)
}

// admit fills the free slots with the threads whose turns are ready, reading no more of them than there are slots.
func (o *Orchestrator) admit() {
	free := o.cfg.Slots - len(o.slotByThread)
	if free <= 0 || o.ctx.Err() != nil {
		return
	}
	var full []string
	for user, held := range o.slotsOfUser {
		if held >= o.cfg.UserSlots {
			full = append(full, user)
		}
	}
	ids, err := o.cfg.DB.ReadyThreads(free, full)
	if err != nil {
		o.ev.Fault("", "", "threads ready for a slot could not be listed", err)
		return
	}
	for _, id := range ids {
		o.reconcile(id)
	}
}

// recover puts right what a stopped process left: the turns it was running leave their slots,
// and each thread it may have stranded is reconciled once.
func (o *Orchestrator) recover() error {
	stranded, err := o.cfg.DB.StrandedThreads()
	if err != nil {
		return fmt.Errorf("orchestrator: stranded threads: %w", err)
	}
	for _, id := range stranded {
		closed, err := o.cfg.DB.RecoverItems(id)
		if err != nil {
			return fmt.Errorf("orchestrator: recover thread %s: %w", id, err)
		}
		if len(closed) > 0 {
			slog.Warn("items cut off by the restart closed", "thread", id, "items", len(closed))
		}
		turn, err := o.cfg.DB.ActiveTurn(id)
		if err != nil {
			return fmt.Errorf("orchestrator: recover thread %s: %w", id, err)
		}
		if turn.Status == db.TurnInProgress {
			if _, err := o.cfg.DB.Park(turn.ID); err != nil {
				return fmt.Errorf("orchestrator: recover thread %s: %w", id, err)
			}
		}
		o.reconcile(id)
	}
	o.admit()
	return nil
}

type slot struct {
	user   string
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

func (o *Orchestrator) launch(user, threadID, turnID string) {
	ctx, cancel := context.WithCancel(o.ctx)
	sl := &slot{user: user, turnID: turnID, ctx: ctx, cancel: cancel}
	o.slotByThread[threadID] = sl
	o.slotsOfUser[user]++
	o.done.Add(1)
	go func() {
		defer o.done.Done()
		defer func() {
			if p := recover(); p != nil {
				slog.Error("turn crashed", "thread", threadID, "turn", turnID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				o.post(func() { o.afterRun(threadID, sl, false, errCrashed) })
			}
		}()
		o.run(threadID, sl)
	}()
}

func (o *Orchestrator) release(threadID string) {
	if sl, exists := o.slotByThread[threadID]; exists {
		sl.cancel()
		delete(o.slotByThread, threadID)
		if o.slotsOfUser[sl.user]--; o.slotsOfUser[sl.user] == 0 {
			delete(o.slotsOfUser, sl.user)
		}
	}
}

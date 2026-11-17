package orchestrator

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) reconcile(threadID string) {
	if _, busy := o.slotByThread[threadID]; busy || o.ctx.Err() != nil {
		return
	}
	st, err := o.store.ThreadState(threadID)
	if err != nil {
		o.halt(threadID)
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
	if turn.Status != db.TurnReady || len(o.slotByThread) >= o.slots || o.slotsOfUser[st.Thread.UserID] >= o.userSlots {
		return
	}
	if err := o.store.Launch(turn.ID); err != nil {
		o.halt(threadID)
		return
	}
	o.launch(st.Thread.UserID, threadID, turn.ID)
}

func (o *Orchestrator) admit() {
	free := o.slots - len(o.slotByThread)
	if free <= 0 || o.ctx.Err() != nil {
		return
	}
	var full []string
	for user, held := range o.slotsOfUser {
		if held >= o.userSlots {
			full = append(full, user)
		}
	}
	ids, err := o.store.ReadyThreads(free, full)
	if err != nil {
		o.ev.Fault("", "", "threads ready for a slot could not be listed")
		return
	}
	for _, id := range ids {
		o.reconcile(id)
	}
}

func (o *Orchestrator) recover() error {
	stranded, err := o.store.StrandedThreads()
	if err != nil {
		return fmt.Errorf("orchestrator: stranded threads: %w", err)
	}
	for _, id := range stranded {
		if _, err := o.store.RecoverItems(id); err != nil {
			return fmt.Errorf("orchestrator: recover thread %s: %w", id, err)
		}
		turn, err := o.store.ActiveTurn(id)
		if err != nil {
			return fmt.Errorf("orchestrator: recover thread %s: %w", id, err)
		}
		if turn.Status == db.TurnInProgress {
			if _, err := o.store.Park(turn.ID); err != nil {
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
		final, err := false, errCrashed
		defer func() {
			recover()
			o.post(finished{threadID: threadID, sl: sl, final: final, err: err})
		}()
		final, err = o.run(threadID, sl)
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

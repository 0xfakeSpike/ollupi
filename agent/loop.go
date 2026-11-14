package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

var (
	errStopped = errors.New("agent: the orchestrator is shutting down")
	errCrashed = errors.New("agent: an internal error stopped this")
)

type slot struct {
	user   string
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

func (o *Orchestrator) Wait() {
	o.done.Wait()
	if o.owner != "" {
		if err := o.cfg.DB.ReleaseShard(o.owner); err != nil {
			slog.Warn("shard lease not released", "err", err)
		}
	}
}

func (o *Orchestrator) loop() {
	defer o.done.Done()
	for {
		select {
		case <-o.ctx.Done():
			return
		case fn := <-o.events:
			o.step(fn)
		}
	}
}

func (o *Orchestrator) step(fn func()) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("orchestrator loop step crashed", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	fn()
	o.admit()
}

func (o *Orchestrator) post(fn func()) bool {
	select {
	case o.events <- fn:
		return true
	case <-o.ctx.Done():
		return false
	}
}

func (o *Orchestrator) call(fn func() error) error {
	done := make(chan error, 1)
	posted := o.post(func() {
		err := errCrashed
		defer func() { done <- err }()
		err = fn()
	})
	if !posted {
		return errStopped
	}
	select {
	case err := <-done:
		return err
	case <-o.ctx.Done():
		return errStopped
	}
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

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

var (
	errStopped = errors.New("agent: the agent is shutting down")
	errCrashed = errors.New("agent: an internal error stopped this")
)

type slot struct {
	user   string
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *Agent) Wait() {
	a.done.Wait()
	if a.owner != "" {
		if err := a.cfg.DB.ReleaseShard(a.owner); err != nil {
			slog.Warn("shard lease not released", "err", err)
		}
	}
}

func (a *Agent) loop() {
	defer a.done.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case fn := <-a.events:
			a.step(fn)
		}
	}
}

func (a *Agent) step(fn func()) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("agent loop step crashed", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	fn()
	a.admit()
}

func (a *Agent) post(fn func()) bool {
	select {
	case a.events <- fn:
		return true
	case <-a.ctx.Done():
		return false
	}
}

func (a *Agent) call(fn func() error) error {
	done := make(chan error, 1)
	posted := a.post(func() {
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
	case <-a.ctx.Done():
		return errStopped
	}
}

func (a *Agent) launch(user, threadID, turnID string) {
	ctx, cancel := context.WithCancel(a.ctx)
	sl := &slot{user: user, turnID: turnID, ctx: ctx, cancel: cancel}
	a.slotByThread[threadID] = sl
	a.slotsOfUser[user]++
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		defer func() {
			if p := recover(); p != nil {
				slog.Error("turn crashed", "thread", threadID, "turn", turnID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				a.post(func() { a.afterRun(threadID, sl, false, errCrashed) })
			}
		}()
		a.run(threadID, sl)
	}()
}

func (a *Agent) release(threadID string) {
	if sl, exists := a.slotByThread[threadID]; exists {
		sl.cancel()
		delete(a.slotByThread, threadID)
		if a.slotsOfUser[sl.user]--; a.slotsOfUser[sl.user] == 0 {
			delete(a.slotsOfUser, sl.user)
		}
	}
}

package agent

import (
	"context"
	"errors"
)

var errStopped = errors.New("agent: the agent is shutting down")

type slot struct {
	turnID string
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *Agent) start() {
	a.done.Add(1)
	go a.loop()
}

func (a *Agent) Wait() {
	a.done.Wait()
}

func (a *Agent) loop() {
	defer a.done.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case fn := <-a.events:
			fn()
			a.admit()
		}
	}
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
	if !a.post(func() { done <- fn() }) {
		return errStopped
	}
	select {
	case err := <-done:
		return err
	case <-a.ctx.Done():
		return errStopped
	}
}

func (a *Agent) enqueue(threadID, turnID string) {
	ctx, cancel := context.WithCancel(a.ctx)
	sl := &slot{turnID: turnID, ctx: ctx, cancel: cancel}
	a.slotByThread[threadID] = sl
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		a.run(threadID, sl)
	}()
}

func (a *Agent) release(threadID string) {
	if sl, exists := a.slotByThread[threadID]; exists {
		sl.cancel()
		delete(a.slotByThread, threadID)
	}
}

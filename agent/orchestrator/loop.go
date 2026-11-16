package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

var (
	errStopped = errors.New("orchestrator: the orchestrator is shutting down")
	errCrashed = errors.New("orchestrator: an internal error stopped this")
)

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

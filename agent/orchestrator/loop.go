package orchestrator

import (
	"errors"
)

var (
	errStopped = errors.New("orchestrator: the orchestrator is shutting down")
	errCrashed = errors.New("orchestrator: an internal error stopped this")
)

func (o *Orchestrator) Wait() {
	o.done.Wait()
	if o.owner != "" {
		o.store.ReleaseShard(o.owner) // a claim not released lapses with its lease
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
	defer func() { recover() }() // a step that panics is dropped, so that the loop goes on
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

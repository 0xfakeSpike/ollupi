package orchestrator

import (
	"errors"
)

var (
	errStopped = errors.New("orchestrator: the orchestrator is shutting down")
	errCrashed = errors.New("orchestrator: an internal error stopped this")
)

type wake struct{ threadID string }

type started struct{ threadID string }

type interrupt struct {
	threadID, turnID string
	reply            chan error
}

type finished struct {
	threadID string
	sl       *slot
	final    bool
	err      error
}

func (o *Orchestrator) Wait() { o.done.Wait() }

func (o *Orchestrator) loop() {
	defer o.done.Done()
	for {
		select {
		case <-o.ctx.Done():
			return
		case m := <-o.inbox:
			o.handle(m)
		}
	}
}

func (o *Orchestrator) handle(m any) {
	defer func() { recover() }() // a message whose handling panics is dropped, so that the loop goes on
	switch m := m.(type) {
	case wake:
		o.reconcile(m.threadID)
	case started:
		o.announce(m.threadID)
	case interrupt:
		err := errCrashed
		defer func() { m.reply <- err }()
		err = o.interrupt(m.threadID, m.turnID)
	case finished:
		o.afterRun(m.threadID, m.sl, m.final, m.err)
	}
	o.admit()
}

func (o *Orchestrator) post(m any) bool {
	select {
	case o.inbox <- m:
		return true
	case <-o.ctx.Done():
		return false
	}
}

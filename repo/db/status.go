package db

import (
	"errors"
	"fmt"
)

// A thread waits on the user when it or one of its running subagents does.
func (d *DB) ThreadStatus(id string) (ThreadStatus, error) {
	turn, err := d.ActiveTurn(id)
	if errors.Is(err, ErrNotFound) {
		return ThreadIdle, nil
	}
	if err != nil {
		return "", fmt.Errorf("the status of thread %s: %w", id, err)
	}
	switch turn.Status {
	case TurnWaitingOnApproval:
		return ThreadWaitingOnApproval, nil
	case TurnWaitingOnUserInput:
		return ThreadWaitingOnUserInput, nil
	}
	running, err := d.RunningSubagents(turn.ID)
	if err != nil {
		return "", fmt.Errorf("the status of thread %s: %w", id, err)
	}
	for _, child := range running {
		cs, err := d.ThreadStatus(child)
		if err != nil {
			return "", err
		}
		if cs == ThreadWaitingOnApproval || cs == ThreadWaitingOnUserInput {
			return cs, nil
		}
	}
	return ThreadActive, nil
}

// A subagent's status shows in the conversation that launched it.
func (d *DB) TopThreadStatus(id string) (ThreadRow, ThreadStatus, error) {
	t, err := d.ThreadRow(id)
	if err != nil {
		return ThreadRow{}, "", err
	}
	if t.ParentThreadID != "" {
		return d.TopThreadStatus(string(t.ParentThreadID))
	}
	status, err := d.ThreadStatus(id)
	return t, status, err
}

package orchestrator

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (o *Orchestrator) workspaceOf(id db.Ref) (db.WorkspaceRow, error) {
	if id == "" {
		return db.WorkspaceRow{}, nil
	}
	w, err := o.cfg.DB.Workspace(string(id))
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("orchestrator: workspace %s: %w", id, err)
	}
	return w, nil
}

// workspaceFor reads a workspace the user names; one of another user's does not exist for them.
func (o *Orchestrator) workspaceFor(user string, id db.Ref) (db.WorkspaceRow, error) {
	if id == "" {
		return db.WorkspaceRow{}, nil
	}
	w, err := o.cfg.DB.WorkspaceOf(user, string(id))
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("orchestrator: workspace %s: %w", id, err)
	}
	return w, nil
}

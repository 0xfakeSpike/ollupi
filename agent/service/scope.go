package service

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (sv *Service) workspaceOf(id db.Ref) (db.WorkspaceRow, error) {
	if id == "" {
		return db.WorkspaceRow{}, nil
	}
	w, err := sv.cfg.DB.Workspace(string(id))
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, err)
	}
	return w, nil
}

// workspaceFor reads a workspace the user names; one of another user's does not exist for them.
func (sv *Service) workspaceFor(user string, id db.Ref) (db.WorkspaceRow, error) {
	if id == "" {
		return db.WorkspaceRow{}, nil
	}
	w, err := sv.cfg.DB.WorkspaceOf(user, string(id))
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, err)
	}
	return w, nil
}

package service

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (sv *Service) UpdateUserPermissions(user string, settings permission.Settings) (db.SettingsRow, error) {
	return sv.permissions.SetUser(user, func(permission.Settings) permission.Settings { return settings })
}

func (sv *Service) UpdateThreadSettings(user, threadID string, settings permission.Settings) (db.ThreadRow, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	return sv.permissions.SetThread(t, func(permission.Settings) permission.Settings { return settings })
}

func (sv *Service) UpdateWorkspaceSettings(user, id string, settings permission.Settings) (db.WorkspaceRow, error) {
	w, err := sv.cfg.DB.WorkspaceOf(user, id)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, err)
	}
	return sv.permissions.SetWorkspace(w, func(permission.Settings) permission.Settings { return settings })
}

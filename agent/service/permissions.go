package service

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (sv *Service) UpdateThreadSettings(user, threadID string, settings permission.Settings) (db.ThreadRow, error) {
	return sv.updateThreadSettings(user, threadID, func(permission.Settings) permission.Settings { return settings })
}

// change is checked against the settings as read, and applied again to them as they are when written.
func (sv *Service) updateThreadSettings(user, threadID string, change func(permission.Settings) permission.Settings) (db.ThreadRow, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	w, err := sv.workspaceFor(user, t.WorkspaceID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	if err := sv.validateLayer(user, sv.permissions.Base(w), t.Settings, permission.ThreadLayer(change(t.Settings))); err != nil {
		return db.ThreadRow{}, err
	}
	t, err = sv.cfg.DB.UpdateThreadSettings(user, threadID, change)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadChanged, Thread: t})
	return t, nil
}

func (sv *Service) UpdateWorkspaceSettings(user, id string, settings permission.Settings) (db.WorkspaceRow, error) {
	return sv.updateWorkspaceSettings(user, id, func(permission.Settings) permission.Settings { return settings })
}

// change is checked against the settings as read, and applied again to them as they are when written.
func (sv *Service) updateWorkspaceSettings(user, id string, change func(permission.Settings) permission.Settings) (db.WorkspaceRow, error) {
	w, err := sv.cfg.DB.WorkspaceOf(user, id)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, err)
	}
	if err := sv.validateLayer(user, sv.permissions.Base(db.WorkspaceRow{}), w.Settings, permission.WorkspaceLayer(w.Name, change(w.Settings))); err != nil {
		return db.WorkspaceRow{}, err
	}
	return sv.changeWorkspace(user, id, func(w *db.WorkspaceRow) { w.Settings = change(w.Settings) })
}

func (sv *Service) allowAlways(threadID string, rules []string) error {
	t, err := sv.cfg.DB.ThreadRow(threadID)
	if err != nil {
		return fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	if parent := string(t.ParentThreadID); parent != "" {
		if t, err = sv.cfg.DB.ThreadRow(parent); err != nil {
			return fmt.Errorf("service: parent thread %s: %w", parent, err)
		}
	}
	allow := permission.Settings{Rules: permission.Rules{Allow: rules}}
	widen := func(s permission.Settings) permission.Settings { return permission.Resolve(s, allow) }
	if t.WorkspaceID == "" {
		_, err := sv.updateThreadSettings(t.UserID, t.ID, widen)
		return err
	}
	_, err = sv.updateWorkspaceSettings(t.UserID, string(t.WorkspaceID), widen)
	return err
}

func (sv *Service) validateLayer(user string, base []permission.Layer, previous permission.Settings, layer permission.Layer) error {
	registry, err := sv.res.Registry(user)
	if err != nil {
		return err
	}
	if err := permission.ValidateLayer(base, previous, layer, registry); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

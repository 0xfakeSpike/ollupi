package role

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (rt *Runtime) Load(turnID string) error {
	turn, err := rt.store.Turn(turnID)
	if err != nil {
		return fmt.Errorf("role: turn %s: %w", turnID, err)
	}
	thread, err := rt.store.ThreadRow(turn.ThreadID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", turn.ThreadID, err)
	}
	items, err := rt.store.RunItems(thread.ID, turn.Seq)
	if err != nil {
		return fmt.Errorf("role: the items of thread %s: %w", thread.ID, err)
	}
	rt.turn, rt.history, rt.items = turn, nil, nil
	for _, it := range items {
		if it.TurnID == turn.ID {
			rt.items = append(rt.items, it)
		} else {
			rt.history = append(rt.history, it)
		}
	}
	settings, err := rt.store.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.redactor, rt.masking = redact.New(settings.RedactionKey), settings.Redaction
	return rt.permitIn(thread)
}

// The settings may have changed, so the permissions are made anew.
func (rt *Runtime) reload() error {
	items, err := rt.store.TurnItems(rt.turn.ID)
	if err != nil {
		return fmt.Errorf("role: reload the turn: %w", err)
	}
	thread, err := rt.store.ThreadRow(rt.thread.ID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", rt.thread.ID, err)
	}
	settings, err := rt.store.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.items, rt.masking = items, settings.Redaction
	return rt.permitIn(thread)
}

func (rt *Runtime) permitIn(t db.ThreadRow) error {
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = rt.store.Workspace(string(t.WorkspaceID)); err != nil {
			return fmt.Errorf("role: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	conversation := permission.ThreadLayer(t.Settings)
	if t.ParentThreadID != "" {
		parent, err := rt.store.ThreadRow(string(t.ParentThreadID))
		if err != nil {
			return fmt.Errorf("role: parent thread %s: %w", t.ParentThreadID, err)
		}
		conversation = permission.ParentLayer(parent.Settings)
	}
	if err := rt.Permit(w, conversation); err != nil {
		return err
	}
	rt.thread, rt.workspace = t, w
	return nil
}

func (rt *Runtime) Permit(workspace db.WorkspaceRow, conversation ...permission.Layer) error {
	checker, err := rt.permissions.Checker(rt.role.own, workspace, rt.registry, conversation...)
	if err != nil {
		return err
	}
	rt.checker = checker
	return nil
}

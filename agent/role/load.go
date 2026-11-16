package role

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// Load reads the turn the runtime runs from DB: its conversation and the conversation's items up to it, and the
// user's settings, which keep their secrets from the model; and makes the role's permissions there with the tools of
// Registry.
func (rt *Runtime) Load(turnID string) error {
	turn, err := rt.DB.Turn(turnID)
	if err != nil {
		return fmt.Errorf("role: turn %s: %w", turnID, err)
	}
	thread, err := rt.DB.ThreadRow(turn.ThreadID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", turn.ThreadID, err)
	}
	items, err := rt.DB.RunItems(thread.ID, turn.Seq)
	if err != nil {
		return fmt.Errorf("role: the items of thread %s: %w", thread.ID, err)
	}
	// The conversation is the turns before this one, its history, and this turn's own items.
	rt.turn, rt.history, rt.items = turn, nil, nil
	for _, it := range items {
		if it.TurnID == turn.ID {
			rt.items = append(rt.items, it)
		} else {
			rt.history = append(rt.history, it)
		}
	}
	settings, err := rt.DB.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.redactor, rt.masking = redact.New(settings.RedactionKey), settings.Redaction
	return rt.permitIn(thread)
}

// reload reads the turn's items, its conversation and the user's settings again, and makes the role's permissions
// anew, as the settings may have changed.
func (rt *Runtime) reload() error {
	items, err := rt.DB.TurnItems(rt.turn.ID)
	if err != nil {
		return fmt.Errorf("role: reload the turn: %w", err)
	}
	thread, err := rt.DB.ThreadRow(rt.thread.ID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", rt.thread.ID, err)
	}
	settings, err := rt.DB.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.items, rt.masking = items, settings.Redaction
	return rt.permitIn(thread)
}

// permitIn makes the role's permissions in the conversation t, with its workspace's, and the conversation's
// settings, or for a subagent its parent's, and places the role there.
func (rt *Runtime) permitIn(t db.ThreadRow) error {
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = rt.DB.Workspace(string(t.WorkspaceID)); err != nil {
			return fmt.Errorf("role: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	conversation := permission.ThreadLayer(t.Settings)
	if t.ParentThreadID != "" {
		parent, err := rt.DB.ThreadRow(string(t.ParentThreadID))
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

// Permit makes the role's permissions for the run, as the deployment's make them with what the role itself keeps of
// the tools, in workspace and the conversation (none outside one).
func (rt *Runtime) Permit(workspace db.WorkspaceRow, conversation ...permission.Layer) error {
	checker, err := rt.permissions.Checker(rt.role.own, workspace, rt.Registry, conversation...)
	if err != nil {
		return err
	}
	rt.checker = checker
	return nil
}

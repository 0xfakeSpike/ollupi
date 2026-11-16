package role

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// Load reads the turn the runtime runs from DB: its conversation and the conversation's items up to it, and the
// user's settings, which keep their secrets from the model; and makes the role's permissions there with the tools of
// Registry.
func (rt *Runtime) Load(turnID string) error {
	store := rt.DB
	turn, err := store.Turn(turnID)
	if err != nil {
		return fmt.Errorf("role: turn %s: %w", turnID, err)
	}
	thread, err := store.ThreadRow(turn.ThreadID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", turn.ThreadID, err)
	}
	items, err := store.RunItems(thread.ID, turn.Seq)
	if err != nil {
		return fmt.Errorf("role: the items of thread %s: %w", thread.ID, err)
	}
	rt.start(turn, items)
	settings, err := store.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.redactor, rt.masking = redact.New(settings.RedactionKey), settings.Redaction
	return rt.permitIn(thread)
}

// reload reads the turn's items, its conversation and the user's settings again, and makes the role's permissions
// anew, as the settings may have changed.
func (rt *Runtime) reload() error {
	store := rt.DB
	items, err := store.TurnItems(rt.turn.ID)
	if err != nil {
		return fmt.Errorf("role: reload the turn: %w", err)
	}
	thread, err := store.ThreadRow(rt.thread.ID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", rt.thread.ID, err)
	}
	settings, err := store.Settings(thread.UserID)
	if err != nil {
		return fmt.Errorf("role: the settings of the user: %w", err)
	}
	rt.items, rt.masking = items, settings.Redaction
	return rt.permitIn(thread)
}

// permitIn makes the role's permissions in the conversation t, with its workspace's, and the conversation's
// settings, or for a subagent its parent's, and places the role there.
func (rt *Runtime) permitIn(t db.ThreadRow) error {
	store := rt.DB
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = store.Workspace(string(t.WorkspaceID)); err != nil {
			return fmt.Errorf("role: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	conversation := permission.ThreadLayer(t.Settings)
	if t.ParentThreadID != "" {
		parent, err := store.ThreadRow(string(t.ParentThreadID))
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

// Permit makes the role's permissions for the run: what the role itself keeps of the tools, below the deployment's
// default settings, the workspace's and the conversation's (none outside one), which decide over it, with the tools of
// Registry the deployment allows.
func (rt *Runtime) Permit(workspace db.WorkspaceRow, conversation ...permission.Layer) error {
	layers := slices.Concat(rt.role.own, permission.Base(rt.settings, rt.shared, workspace), conversation)
	var allowlist []string
	if rt.tools != nil {
		allowlist = []string{}
		for _, d := range rt.Registry.Descriptors() {
			if slices.Contains(rt.tools, d.Name) {
				allowlist = append(allowlist, d.Name)
			}
		}
	}
	checker, err := permission.New(layers, allowlist, rt.Registry)
	if err != nil {
		return err
	}
	rt.checker = checker
	return nil
}

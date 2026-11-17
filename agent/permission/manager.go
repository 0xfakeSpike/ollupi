package permission

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Manager keeps the permission settings of threads and workspaces: a change is checked against the layers it sits on,
// written, and told to those who watch.
type Manager struct {
	deployment Deployment
	store      *db.DB
	registry   func(user string) (*tools.Registry, error)
	events     *event.Emitter
}

func NewManager(deployment Deployment, store *db.DB, registry func(user string) (*tools.Registry, error), events *event.Emitter) *Manager {
	return &Manager{deployment: deployment, store: store, registry: registry, events: events}
}

func (m *Manager) CheckThread(user string, w db.WorkspaceRow, previous, next Settings) error {
	return m.validate(user, m.deployment.Base(w), previous, ThreadLayer(next))
}

// change is checked against the settings as read, and applied again to them as they are when written.
func (m *Manager) SetThread(t db.ThreadRow, change func(Settings) Settings) (db.ThreadRow, error) {
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = m.store.WorkspaceOf(t.UserID, string(t.WorkspaceID)); err != nil {
			return db.ThreadRow{}, fmt.Errorf("permission: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	if err := m.CheckThread(t.UserID, w, t.Settings, change(t.Settings)); err != nil {
		return db.ThreadRow{}, err
	}
	changed, err := m.store.UpdateThreadSettings(t.UserID, t.ID, change)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("permission: thread %s: %w", t.ID, err)
	}
	m.events.Emit(event.Event{Kind: event.ThreadChanged, Thread: changed})
	return changed, nil
}

// change is checked against the settings as read, and applied again to them as they are when written.
func (m *Manager) SetWorkspace(w db.WorkspaceRow, change func(Settings) Settings) (db.WorkspaceRow, error) {
	if err := m.validate(w.UserID, m.deployment.Base(db.WorkspaceRow{}), w.Settings, WorkspaceLayer(w.Name, change(w.Settings))); err != nil {
		return db.WorkspaceRow{}, err
	}
	changed, err := m.store.UpdateWorkspace(w.UserID, w.ID, func(row db.WorkspaceRow) db.WorkspaceRow {
		row.Settings, row.UpdatedAt = change(row.Settings), time.Now().UTC()
		return row
	})
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("permission: workspace %s: %w", w.ID, err)
	}
	m.events.Emit(event.Event{Kind: event.WorkspaceChanged, Workspace: &changed})
	return changed, nil
}

// The rules are kept where the conversation's permissions come from: its workspace, or else the conversation itself,
// and a subagent's in its parent's.
func (m *Manager) AllowAlways(threadID string, rules []string) error {
	t, err := m.store.ThreadRow(threadID)
	if err != nil {
		return fmt.Errorf("permission: thread %s: %w", threadID, err)
	}
	if parent := string(t.ParentThreadID); parent != "" {
		if t, err = m.store.ThreadRow(parent); err != nil {
			return fmt.Errorf("permission: parent thread %s: %w", parent, err)
		}
	}
	allow := Settings{Rules: Rules{Allow: rules}}
	widen := func(s Settings) Settings { return Resolve(s, allow) }
	if t.WorkspaceID == "" {
		_, err := m.SetThread(t, widen)
		return err
	}
	w, err := m.store.WorkspaceOf(t.UserID, string(t.WorkspaceID))
	if err != nil {
		return fmt.Errorf("permission: workspace %s: %w", t.WorkspaceID, err)
	}
	_, err = m.SetWorkspace(w, widen)
	return err
}

func (m *Manager) validate(user string, base []Layer, previous Settings, layer Layer) error {
	registry, err := m.registry(user)
	if err != nil {
		return err
	}
	return validateLayer(base, previous, layer, registry)
}

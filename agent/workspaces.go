package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (a *Agent) AddWorkspace(path, name string) (repo.WorkspaceRow, error) {
	root, err := sandbox.CanonicalDirectory(path)
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w", err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if name, err = workspaceName(name); err != nil {
		return repo.WorkspaceRow{}, err
	}
	now := time.Now().UTC()
	w := repo.WorkspaceRow{ID: uuid.NewString(), Name: name, RootPath: root, CreatedAt: now, UpdatedAt: now}
	w, err = a.cfg.DB.AddWorkspace(w,
		func(existing repo.WorkspaceRow) bool { return sameDirectory(existing.RootPath, root) },
		func(existing repo.WorkspaceRow) (repo.WorkspaceRow, bool) {
			if existing.ArchivedAt == nil {
				return existing, false
			}
			existing.ArchivedAt, existing.UpdatedAt = nil, now
			return existing, true
		})
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	a.emit(Event{Kind: EventWorkspaceChanged, Workspace: &w})
	return w, nil
}

func (a *Agent) RenameWorkspace(id, name string) (repo.WorkspaceRow, error) {
	name, err := workspaceName(name)
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	return a.changeWorkspace(id, func(w *repo.WorkspaceRow) { w.Name = name })
}

func (a *Agent) ArchiveWorkspace(id string, archived bool) (repo.WorkspaceRow, error) {
	return a.changeWorkspace(id, func(w *repo.WorkspaceRow) {
		w.ArchivedAt = nil
		if archived {
			at := w.UpdatedAt
			w.ArchivedAt = &at
		}
	})
}

func (a *Agent) UpdateWorkspaceSettings(id string, settings permission.Settings) (repo.WorkspaceRow, error) {
	w, err := a.workspaceOf(repo.Ref(id))
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	if err := a.validateLayer(a.layers(repo.WorkspaceRow{}), w.Settings, workspaceLayer(w.Name, settings)); err != nil {
		return repo.WorkspaceRow{}, err
	}
	return a.changeWorkspace(id, func(w *repo.WorkspaceRow) { w.Settings = settings })
}

func (a *Agent) changeWorkspace(id string, change func(*repo.WorkspaceRow)) (repo.WorkspaceRow, error) {
	w, err := a.cfg.DB.UpdateWorkspace(id, func(w repo.WorkspaceRow) (repo.WorkspaceRow, error) {
		w.UpdatedAt = time.Now().UTC()
		change(&w)
		return w, nil
	})
	if errors.Is(err, repo.ErrNotFound) {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: workspace %s: %w", id, repo.ErrNotFound)
	}
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	a.emit(Event{Kind: EventWorkspaceChanged, Workspace: &w})
	return w, nil
}

func (a *Agent) validateLayer(base []permission.Layer, previous permission.Settings, layer permission.Layer) error {
	if a.registry == nil {
		return fmt.Errorf("agent: the tool registry is not ready")
	}
	if err := permission.ValidateLayer(base, previous, layer, a.registry); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

func usable(w repo.WorkspaceRow) error {
	if w.ArchivedAt != nil {
		return fmt.Errorf("agent: workspace %s: %w", w.Name, ErrWorkspaceArchived)
	}
	return nil
}

func workspaceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return "", fmt.Errorf("agent: %w: a workspace name must be 1 to 120 characters", ErrInvalidName)
	}
	return name, nil
}

func sameDirectory(a, b string) bool {
	if a == b {
		return true
	}
	canonical, err := sandbox.CanonicalDirectory(a)
	if err != nil || canonical != a {
		return false
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && os.SameFile(left, right)
}

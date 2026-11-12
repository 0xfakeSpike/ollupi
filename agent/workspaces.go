package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

// workspaceTimeout bounds what adding or moving a workspace asks of the user's sandbox, the move itself aside.
const workspaceTimeout = time.Minute

func (a *Agent) AddWorkspace(ctx context.Context, user, path, name string) (repo.WorkspaceRow, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := a.cfg.BoxFor(user)
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: the sandbox of the user: %w", err)
	}
	root, err := box.Directory(ctx, path)
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %v", ErrInvalidPath, err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if name, err = workspaceName(name); err != nil {
		return repo.WorkspaceRow{}, err
	}
	now := time.Now().UTC()
	w := repo.WorkspaceRow{ID: uuid.NewString(), UserID: user, Name: name, RootPath: root, CreatedAt: now, UpdatedAt: now}
	w, err = a.cfg.DB.AddWorkspace(w,
		func(existing repo.WorkspaceRow) bool { return box.Same(ctx, existing.RootPath, root) },
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

func (a *Agent) RenameWorkspace(user, id, name string) (repo.WorkspaceRow, error) {
	name, err := workspaceName(name)
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	return a.changeWorkspace(user, id, func(w *repo.WorkspaceRow) { w.Name = name })
}

// MoveWorkspace moves the workspace's folder to path, a folder that is there and empty, and points the workspace at
// it. Every thread, memory and task keeps its workspace id, so nothing else is rewritten. A turn in progress would keep
// calling tools against the folder it started in, so the move is refused until the workspace's conversations are
// idle, and none starts a turn while the folder moves.
func (a *Agent) MoveWorkspace(ctx context.Context, user, id, path string) (repo.WorkspaceRow, error) {
	// The move itself outlives the request: a folder left half moved because a client went away helps no one.
	move := context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	w, err := a.workspaceFor(user, repo.Ref(id))
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	if w.ID == "" {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: workspace %s: %w", id, repo.ErrNotFound)
	}
	box, err := a.cfg.BoxFor(user)
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: the sandbox of the user: %w", err)
	}
	target, err := box.Directory(ctx, path)
	if err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %v", ErrInvalidPath, err)
	}
	if box.Same(ctx, w.RootPath, target) {
		return w, nil
	}
	if sandbox.Within(w.RootPath, target) {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %q is inside the workspace", ErrInvalidPath, target)
	}
	if err := a.startMove(user, w); err != nil {
		return repo.WorkspaceRow{}, err
	}
	defer func() {
		a.moveMu.Lock()
		delete(a.moving, w.ID)
		a.moveMu.Unlock()
	}()
	if entries, err := box.ReadDir(ctx, target); err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %v", ErrInvalidPath, err)
	} else if len(entries) > 0 {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %q is not empty", ErrInvalidPath, target)
	}
	rows, err := a.cfg.DB.Workspaces(user)
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	for _, other := range rows {
		if other.ID != w.ID && box.Same(ctx, other.RootPath, target) {
			return repo.WorkspaceRow{}, fmt.Errorf("agent: %w: %q is the folder of the workspace %s", ErrInvalidPath, target, other.Name)
		}
	}
	if err := box.Move(move, w.RootPath, target); err != nil {
		return repo.WorkspaceRow{}, fmt.Errorf("agent: %w", err)
	}
	moved, err := a.cfg.DB.SetWorkspaceRoot(user, w.ID, target, time.Now().UTC())
	if err != nil {
		if back := box.Move(move, target, w.RootPath); back != nil {
			return repo.WorkspaceRow{}, fmt.Errorf("agent: the folder is now at %s but the workspace still names %s: %w; moving it back failed too: %v", target, w.RootPath, err, back)
		}
		return repo.WorkspaceRow{}, err
	}
	a.emit(Event{Kind: EventWorkspaceChanged, Workspace: &moved})
	return moved, nil
}

// startMove claims the workspace for a move once none of its conversations has a turn open. submit checks the claim
// under the same lock, so no turn starts between the check and the claim.
func (a *Agent) startMove(user string, w repo.WorkspaceRow) error {
	a.moveMu.Lock()
	defer a.moveMu.Unlock()
	if a.moving[w.ID] {
		return fmt.Errorf("agent: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
	}
	busy, err := a.cfg.DB.WorkspaceBusy(user, w.ID)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("agent: workspace %s: %w", w.Name, ErrWorkspaceBusy)
	}
	a.moving[w.ID] = true
	return nil
}

func (a *Agent) ArchiveWorkspace(user, id string, archived bool) (repo.WorkspaceRow, error) {
	return a.changeWorkspace(user, id, func(w *repo.WorkspaceRow) {
		w.ArchivedAt = nil
		if archived {
			at := w.UpdatedAt
			w.ArchivedAt = &at
		}
	})
}

func (a *Agent) UpdateWorkspaceSettings(user, id string, settings permission.Settings) (repo.WorkspaceRow, error) {
	w, err := a.workspaceFor(user, repo.Ref(id))
	if err != nil {
		return repo.WorkspaceRow{}, err
	}
	if err := a.validateLayer(user, a.layers(repo.WorkspaceRow{}), w.Settings, workspaceLayer(w.Name, settings)); err != nil {
		return repo.WorkspaceRow{}, err
	}
	return a.changeWorkspace(user, id, func(w *repo.WorkspaceRow) { w.Settings = settings })
}

func (a *Agent) changeWorkspace(user, id string, change func(*repo.WorkspaceRow)) (repo.WorkspaceRow, error) {
	w, err := a.cfg.DB.UpdateWorkspace(user, id, func(w repo.WorkspaceRow) (repo.WorkspaceRow, error) {
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

func (a *Agent) validateLayer(user string, base []permission.Layer, previous permission.Settings, layer permission.Layer) error {
	if a.registry == nil {
		return fmt.Errorf("agent: the tool registry is not ready")
	}
	registry, err := a.registryFor(user)
	if err != nil {
		return err
	}
	if err := permission.ValidateLayer(base, previous, layer, registry); err != nil {
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

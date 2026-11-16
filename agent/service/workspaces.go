package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

// workspaceTimeout bounds what adding or moving a workspace asks of the user's sandbox, the move itself aside.
const workspaceTimeout = time.Minute

func (sv *Service) AddWorkspace(ctx context.Context, user, path, name string) (db.WorkspaceRow, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := sv.cfg.BoxFor(user)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: the sandbox of the user: %w", err)
	}
	root, err := box.Directory(ctx, path)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w: %v", ErrInvalidPath, err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if name, err = workspaceName(name); err != nil {
		return db.WorkspaceRow{}, err
	}
	now := time.Now().UTC()
	w := db.WorkspaceRow{ID: uuid.NewString(), UserID: user, Name: name, RootPath: root, CreatedAt: now, UpdatedAt: now}
	w, err = sv.cfg.DB.AddWorkspace(w,
		func(existing db.WorkspaceRow) bool { return box.Same(ctx, existing.RootPath, root) },
		func(existing db.WorkspaceRow) (db.WorkspaceRow, bool) {
			if existing.ArchivedAt == nil {
				return existing, false
			}
			existing.ArchivedAt, existing.UpdatedAt = nil, now
			return existing, true
		})
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	sv.ev.Emit(event.Event{Kind: event.WorkspaceChanged, Workspace: &w})
	return w, nil
}

func (sv *Service) RenameWorkspace(user, id, name string) (db.WorkspaceRow, error) {
	name, err := workspaceName(name)
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	return sv.changeWorkspace(user, id, func(w *db.WorkspaceRow) { w.Name = name })
}

// MoveWorkspace moves the workspace's folder to path, a folder that is there and empty, and points the workspace at
// it. Every thread, memory and task keeps its workspace id, so nothing else is rewritten. A turn in progress would keep
// calling tools against the folder it started in, so the move is refused until the workspace's conversations are
// idle, and none starts a turn while the folder moves.
func (sv *Service) MoveWorkspace(ctx context.Context, user, id, path string) (db.WorkspaceRow, error) {
	// The move itself outlives the request: a folder left half moved because a client went away helps no one.
	move := context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	w, err := sv.workspaceFor(user, db.Ref(id))
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	if w.ID == "" {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, db.ErrNotFound)
	}
	box, err := sv.cfg.BoxFor(user)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: the sandbox of the user: %w", err)
	}
	target, err := box.Directory(ctx, path)
	if err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w: %v", ErrInvalidPath, err)
	}
	if box.Same(ctx, w.RootPath, target) {
		return w, nil
	}
	if sandbox.Within(w.RootPath, target) {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w: %q is inside the workspace", ErrInvalidPath, target)
	}
	if err := sv.startMove(user, w); err != nil {
		return db.WorkspaceRow{}, err
	}
	defer func() {
		sv.moveMu.Lock()
		delete(sv.moving, w.ID)
		sv.moveMu.Unlock()
	}()
	if entries, err := box.ReadDir(ctx, target); err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w: %v", ErrInvalidPath, err)
	} else if len(entries) > 0 {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w: %q is not empty", ErrInvalidPath, target)
	}
	rows, err := sv.cfg.DB.Workspaces(user)
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	for _, other := range rows {
		if other.ID != w.ID && box.Same(ctx, other.RootPath, target) {
			return db.WorkspaceRow{}, fmt.Errorf("service: %w: %q is the folder of the workspace %s", ErrInvalidPath, target, other.Name)
		}
	}
	if err := box.Move(move, w.RootPath, target); err != nil {
		return db.WorkspaceRow{}, fmt.Errorf("service: %w", err)
	}
	moved, err := sv.cfg.DB.SetWorkspaceRoot(user, w.ID, target, time.Now().UTC())
	if err != nil {
		if back := box.Move(move, target, w.RootPath); back != nil {
			return db.WorkspaceRow{}, fmt.Errorf("service: the folder is now at %s but the workspace still names %s: %w; moving it back failed too: %v", target, w.RootPath, err, back)
		}
		return db.WorkspaceRow{}, err
	}
	sv.ev.Emit(event.Event{Kind: event.WorkspaceChanged, Workspace: &moved})
	return moved, nil
}

// startMove claims the workspace for a move once none of its conversations has a turn open. submit checks the claim
// under the same lock, so no turn starts between the check and the claim.
func (sv *Service) startMove(user string, w db.WorkspaceRow) error {
	sv.moveMu.Lock()
	defer sv.moveMu.Unlock()
	if sv.moving[w.ID] {
		return fmt.Errorf("service: workspace %s is moving: %w", w.Name, ErrWorkspaceBusy)
	}
	busy, err := sv.cfg.DB.WorkspaceBusy(user, w.ID)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("service: workspace %s: %w", w.Name, ErrWorkspaceBusy)
	}
	sv.moving[w.ID] = true
	return nil
}

func (sv *Service) ArchiveWorkspace(user, id string, archived bool) (db.WorkspaceRow, error) {
	return sv.changeWorkspace(user, id, func(w *db.WorkspaceRow) {
		w.ArchivedAt = nil
		if archived {
			at := w.UpdatedAt
			w.ArchivedAt = &at
		}
	})
}

func (sv *Service) UpdateWorkspaceSettings(user, id string, settings permission.Settings) (db.WorkspaceRow, error) {
	return sv.updateWorkspaceSettings(user, id, func(permission.Settings) permission.Settings { return settings })
}

// updateWorkspaceSettings checks what change makes of the workspace's settings as they were read, and writes what it
// makes of them as they are when written.
func (sv *Service) updateWorkspaceSettings(user, id string, change func(permission.Settings) permission.Settings) (db.WorkspaceRow, error) {
	w, err := sv.workspaceFor(user, db.Ref(id))
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	if err := sv.validateLayer(user, permission.Base(sv.cfg.Settings, sv.res.Shared(), db.WorkspaceRow{}), w.Settings, permission.WorkspaceLayer(w.Name, change(w.Settings))); err != nil {
		return db.WorkspaceRow{}, err
	}
	return sv.changeWorkspace(user, id, func(w *db.WorkspaceRow) { w.Settings = change(w.Settings) })
}

func (sv *Service) changeWorkspace(user, id string, change func(*db.WorkspaceRow)) (db.WorkspaceRow, error) {
	w, err := sv.cfg.DB.UpdateWorkspace(user, id, func(w db.WorkspaceRow) db.WorkspaceRow {
		w.UpdatedAt = time.Now().UTC()
		change(&w)
		return w
	})
	if errors.Is(err, db.ErrNotFound) {
		return db.WorkspaceRow{}, fmt.Errorf("service: workspace %s: %w", id, db.ErrNotFound)
	}
	if err != nil {
		return db.WorkspaceRow{}, err
	}
	sv.ev.Emit(event.Event{Kind: event.WorkspaceChanged, Workspace: &w})
	return w, nil
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

func usable(w db.WorkspaceRow) error {
	if w.ArchivedAt != nil {
		return fmt.Errorf("service: workspace %s: %w", w.Name, ErrWorkspaceArchived)
	}
	return nil
}

func workspaceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return "", fmt.Errorf("service: %w: a workspace name must be 1 to 120 characters", ErrInvalidName)
	}
	return name, nil
}

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

package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
)

const workspaceTimeout = time.Minute

func (a *App) Workspaces(ctx context.Context, u string) ([]user.Workspace, error) {
	var ws []user.Workspace
	err := a.st.Read(ctx, func(r store.Reader) (err error) { ws, err = r.Workspaces(u); return err })
	return ws, err
}

func (a *App) AddWorkspace(ctx context.Context, u, path, name string) (user.Workspace, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := a.boxes(u)
	if err != nil {
		return user.Workspace{}, fmt.Errorf("the sandbox of the user: %w", err)
	}
	root, err := box.Directory(ctx, path)
	if err != nil {
		return user.Workspace{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if name, err = user.WorkspaceName(name); err != nil {
		return user.Workspace{}, err
	}
	existing, err := a.Workspaces(ctx, u)
	if err != nil {
		return user.Workspace{}, err
	}
	i := slices.IndexFunc(existing, func(w user.Workspace) bool { return box.Same(ctx, w.Root, root) })
	now := time.Now().UTC()
	w := user.Workspace{ID: uuid.NewString(), Name: name, Root: root, Created: now, Updated: now}
	if i >= 0 {
		if w = existing[i]; w.Archived == nil {
			return w, nil
		}
		w.Archived, w.Updated = nil, now
	}
	err = a.st.Update(ctx, func(tx store.Tx) error { return tx.PutWorkspace(u, w) })
	return w, err
}

func (a *App) RenameWorkspace(ctx context.Context, u, id, name string) (user.Workspace, error) {
	name, err := user.WorkspaceName(name)
	if err != nil {
		return user.Workspace{}, err
	}
	return a.changeWorkspace(ctx, u, id, func(w *user.Workspace) { w.Name = name })
}

func (a *App) ArchiveWorkspace(ctx context.Context, u, id string, archived bool) (user.Workspace, error) {
	return a.changeWorkspace(ctx, u, id, func(w *user.Workspace) {
		w.Archived = nil
		if archived {
			at := w.Updated
			w.Archived = &at
		}
	})
}

func (a *App) changeWorkspace(ctx context.Context, u, id string, change func(*user.Workspace)) (user.Workspace, error) {
	var w user.Workspace
	err := a.st.Update(ctx, func(tx store.Tx) (err error) {
		if w, err = tx.Workspace(u, id); err != nil {
			return err
		}
		w.Updated = time.Now().UTC()
		change(&w)
		return tx.PutWorkspace(u, w)
	})
	return w, err
}

func (a *App) MoveWorkspace(ctx context.Context, u, id, path string) (user.Workspace, error) {
	move := context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := a.boxes(u)
	if err != nil {
		return user.Workspace{}, fmt.Errorf("the sandbox of the user: %w", err)
	}
	target, err := box.Directory(ctx, path)
	if err != nil {
		return user.Workspace{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	w, all, err := a.claim(ctx, u, id)
	if err != nil {
		return user.Workspace{}, err
	}
	defer func() {
		a.mu.Lock()
		delete(a.moving, w.ID)
		a.mu.Unlock()
	}()
	if box.Same(ctx, w.Root, target) {
		return w, nil
	}
	if err := checkTarget(ctx, box, w, all, target); err != nil {
		return user.Workspace{}, err
	}
	if err := box.Move(move, w.Root, target); err != nil {
		return user.Workspace{}, err
	}
	from := w.Root
	w.Root, w.Updated = target, time.Now().UTC()
	if err := a.st.Update(move, func(tx store.Tx) error { return tx.PutWorkspace(u, w) }); err != nil {
		if back := box.Move(move, target, from); back != nil {
			return user.Workspace{}, fmt.Errorf("the folder is now at %s but the workspace still names %s: %w; moving it back failed too: %v", target, from, err, back)
		}
		return user.Workspace{}, err
	}
	return w, nil
}

func (a *App) claim(ctx context.Context, u, id string) (user.Workspace, []user.Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var w user.Workspace
	var all []user.Workspace
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		if w, err = r.Workspace(u, id); err != nil {
			return err
		}
		if all, err = r.Workspaces(u); err != nil {
			return err
		}
		busy, err := r.WorkspaceBusy(u, id)
		if err != nil {
			return err
		}
		if busy || a.moving[id] {
			return fmt.Errorf("workspace %s: %w", w.Name, ErrWorkspaceBusy)
		}
		return nil
	})
	if err != nil {
		return user.Workspace{}, nil, err
	}
	a.moving[id] = true
	return w, all, nil
}

func checkTarget(ctx context.Context, box sandbox.Box, w user.Workspace, all []user.Workspace, target string) error {
	if sandbox.Within(w.Root, target) {
		return fmt.Errorf("%w: %q is inside the workspace", ErrInvalidPath, target)
	}
	entries, err := box.ReadDir(ctx, target)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %q is not empty", ErrInvalidPath, target)
	}
	for _, other := range all {
		if other.ID != w.ID && box.Same(ctx, other.Root, target) {
			return fmt.Errorf("%w: %q is the folder of the workspace %s", ErrInvalidPath, target, other.Name)
		}
	}
	return nil
}

func (a *App) Environments(ctx context.Context, u string) ([]user.Environment, error) {
	var envs []user.Environment
	err := a.st.Read(ctx, func(r store.Reader) (err error) { envs, err = r.Environments(u); return err })
	return envs, err
}

func (a *App) AddEnvironment(ctx context.Context, u, name string, kind user.EnvironmentKind) (user.Environment, error) {
	name, err := user.EnvironmentName(name)
	if err != nil {
		return user.Environment{}, err
	}
	if kind, err = user.NewEnvironmentKind(kind); err != nil {
		return user.Environment{}, err
	}
	now := time.Now().UTC()
	e := user.Environment{ID: uuid.NewString(), Name: name, Kind: kind, Created: now, Updated: now}
	err = a.st.Update(ctx, func(tx store.Tx) error { return a.putEnvironment(tx, u, e) })
	return e, err
}

func (a *App) RenameEnvironment(ctx context.Context, u, id, name string) (user.Environment, error) {
	name, err := user.EnvironmentName(name)
	if err != nil {
		return user.Environment{}, err
	}
	var e user.Environment
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		if e, err = tx.Environment(u, id); err != nil {
			return err
		}
		e.Name, e.Updated = name, time.Now().UTC()
		return a.putEnvironment(tx, u, e)
	})
	return e, err
}

func (a *App) putEnvironment(tx store.Tx, u string, e user.Environment) error {
	envs, err := tx.Environments(u)
	if err != nil {
		return err
	}
	for _, other := range envs {
		if other.ID != e.ID && strings.EqualFold(other.Name, e.Name) {
			return fmt.Errorf("%w: you already have an environment called %q", user.ErrInvalidName, other.Name)
		}
	}
	return tx.PutEnvironment(u, e)
}

func (a *App) DeleteEnvironment(ctx context.Context, u, id string) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		e, err := tx.Environment(u, id)
		if err != nil {
			return err
		}
		if e.Kind == user.Local {
			return fmt.Errorf("%w: %s is this machine, whose folders the workspaces are", user.ErrInvalidName, e.Name)
		}
		used, err := tx.EnvironmentInUse(u, id)
		if err != nil {
			return err
		}
		if used {
			return fmt.Errorf("environment %s: %w", e.Name, ErrEnvironmentInUse)
		}
		return tx.DeleteEnvironment(u, id)
	})
}

type MCPServer struct {
	user.MCPServer
	Status *user.MCPStatus
}

func (a *App) MCPServers(ctx context.Context, u string) ([]MCPServer, error) {
	var stored []user.MCPServer
	if err := a.st.Read(ctx, func(r store.Reader) (err error) { stored, err = r.MCPServers(u); return err }); err != nil {
		return nil, err
	}
	statuses, err := a.statuses(u)
	if err != nil {
		return nil, err
	}
	out := make([]MCPServer, len(stored))
	for i, s := range stored {
		out[i].MCPServer = s
		if j := slices.IndexFunc(statuses, func(st user.MCPStatus) bool { return st.Name == s.Name }); j >= 0 {
			out[i].Status = &statuses[j]
		}
	}
	return out, nil
}

func (a *App) SetMCPServer(ctx context.Context, u string, s user.MCPServer) error {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return err
	}
	return a.st.Update(ctx, func(tx store.Tx) error {
		mine, err := tx.MCPServers(u)
		if err != nil {
			return err
		}
		var providers []string
		for _, t := range box.Tools {
			mineToo := slices.ContainsFunc(mine, func(m user.MCPServer) bool { return m.Name == t.Provider })
			if !mineToo && !slices.Contains(providers, t.Provider) {
				providers = append(providers, t.Provider)
			}
		}
		if err := s.Check(providers); err != nil {
			return err
		}
		return tx.PutMCPServer(u, s)
	})
}

func (a *App) DeleteMCPServer(ctx context.Context, u, name string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.DeleteMCPServer(u, name) })
}

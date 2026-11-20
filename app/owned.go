package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

const workspaceTimeout = time.Minute

func (a *App) Workspaces(ctx context.Context, user string) ([]account.Workspace, error) {
	var ws []account.Workspace
	err := a.st.Read(ctx, func(r store.Reader) (err error) { ws, err = r.Workspaces(user); return err })
	return ws, err
}

func (a *App) AddWorkspace(ctx context.Context, user, path, name string) (account.Workspace, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := a.boxes(user)
	if err != nil {
		return account.Workspace{}, fmt.Errorf("the sandbox of the user: %w", err)
	}
	root, err := box.Directory(ctx, path)
	if err != nil {
		return account.Workspace{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if name, err = account.Name("a workspace name", name); err != nil {
		return account.Workspace{}, err
	}
	existing, err := a.Workspaces(ctx, user)
	if err != nil {
		return account.Workspace{}, err
	}
	i := slices.IndexFunc(existing, func(w account.Workspace) bool { return box.Same(ctx, w.Root, root) })
	now := time.Now().UTC()
	w := account.Workspace{ID: uuid.NewString(), Name: name, Root: root, Kind: account.ProjectWorkspace, Created: now, Updated: now}
	if i >= 0 {
		if w = existing[i]; w.Archived == nil {
			return w, nil
		}
		w.Archived, w.Updated = nil, now
	}
	err = a.st.Update(ctx, func(tx store.Tx) error { return tx.PutWorkspace(user, w) })
	return w, err
}

func (a *App) RenameWorkspace(ctx context.Context, user, id, name string) (account.Workspace, error) {
	name, err := account.Name("a workspace name", name)
	if err != nil {
		return account.Workspace{}, err
	}
	return a.changeWorkspace(ctx, user, id, func(_ store.Tx, w *account.Workspace) error {
		w.Name = name
		return nil
	})
}

func (a *App) UpdateWorkspaceInstructions(ctx context.Context, user, id, text string) (account.Workspace, error) {
	text, err := account.Instructions(text)
	if err != nil {
		return account.Workspace{}, err
	}
	return a.changeWorkspace(ctx, user, id, func(_ store.Tx, w *account.Workspace) error {
		w.Instructions = text
		return nil
	})
}

func (a *App) UpdateWorkspaceProviders(ctx context.Context, user, id string, p account.Providers) (account.Workspace, error) {
	box, err := a.toolbox(ctx, user)
	if err != nil {
		return account.Workspace{}, err
	}
	for _, name := range slices.Concat(p.Enabled, p.Disabled) {
		known := slices.ContainsFunc(box.Tools, func(t policy.Tool) bool { return t.Provider == name })
		if !known {
			return account.Workspace{}, fmt.Errorf("%w: no tools come from a provider named %q", policy.ErrInvalidSettings, name)
		}
	}
	return a.changeWorkspace(ctx, user, id, func(_ store.Tx, w *account.Workspace) error {
		w.Providers = p
		return nil
	})
}

func (a *App) SetWorkspaceKind(ctx context.Context, user, id string, kind account.WorkspaceKind) (account.Workspace, error) {
	if !slices.Contains(account.WorkspaceKinds, kind) {
		return account.Workspace{}, fmt.Errorf("%w: a workspace is one of %q, not %q", account.ErrInvalidName, account.WorkspaceKinds, kind)
	}
	if kind == account.HubWorkspace {
		box, err := a.toolbox(ctx, user)
		if err != nil {
			return account.Workspace{}, err
		}
		offered := slices.ContainsFunc(box.Tools, func(t policy.Tool) bool { return t.Provider == agent.HubProvider })
		if !offered {
			return account.Workspace{}, ErrNoHub
		}
	}
	return a.changeWorkspace(ctx, user, id, func(tx store.Tx, w *account.Workspace) error {
		all, err := tx.Workspaces(user)
		if err != nil {
			return err
		}
		otherHub := slices.ContainsFunc(all, func(o account.Workspace) bool { return o.ID != w.ID && o.Kind == account.HubWorkspace })
		if kind == account.HubWorkspace && otherHub {
			return ErrSecondHub
		}
		w.Kind = kind
		return nil
	})
}

func (a *App) ArchiveWorkspace(ctx context.Context, user, id string, archived bool) (account.Workspace, error) {
	return a.changeWorkspace(ctx, user, id, func(_ store.Tx, w *account.Workspace) error {
		w.Archived = nil
		if archived {
			at := w.Updated
			w.Archived = &at
		}
		return nil
	})
}

func (a *App) changeWorkspace(ctx context.Context, user, id string, change func(store.Tx, *account.Workspace) error) (account.Workspace, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (account.Workspace, error) { return tx.Workspace(user, id) },
		func(tx store.Tx, w *account.Workspace) error {
			w.Updated = time.Now().UTC()
			return change(tx, w)
		},
		func(tx store.Tx, w account.Workspace) error { return tx.PutWorkspace(user, w) })
}

func (a *App) MoveWorkspace(ctx context.Context, user, id, path string) (account.Workspace, error) {
	move := context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	box, err := a.boxes(user)
	if err != nil {
		return account.Workspace{}, fmt.Errorf("the sandbox of the user: %w", err)
	}
	target, err := box.Directory(ctx, path)
	if err != nil {
		return account.Workspace{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	w, all, err := a.claimForMove(ctx, user, id)
	if err != nil {
		return account.Workspace{}, err
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
		return account.Workspace{}, err
	}
	if err := box.Move(move, w.Root, target); err != nil {
		return account.Workspace{}, err
	}
	from := w.Root
	w.Root, w.Updated = target, time.Now().UTC()
	if err := a.st.Update(move, func(tx store.Tx) error { return tx.PutWorkspace(user, w) }); err != nil {
		if back := box.Move(move, target, from); back != nil {
			return account.Workspace{}, fmt.Errorf("the folder is now at %s but the workspace still names %s: %w; moving it back failed too: %v", target, from, err, back)
		}
		return account.Workspace{}, err
	}
	return w, nil
}

func (a *App) claimForMove(ctx context.Context, user, id string) (account.Workspace, []account.Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var w account.Workspace
	var all []account.Workspace
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		if w, err = r.Workspace(user, id); err != nil {
			return err
		}
		if all, err = r.Workspaces(user); err != nil {
			return err
		}
		busy, err := r.WorkspaceBusy(user, id)
		if err != nil {
			return err
		}
		if busy || a.moving[id] {
			return fmt.Errorf("workspace %s: %w", w.Name, ErrWorkspaceBusy)
		}
		return nil
	})
	if err != nil {
		return account.Workspace{}, nil, err
	}
	a.moving[id] = true
	return w, all, nil
}

func checkTarget(ctx context.Context, box sandbox.Box, w account.Workspace, all []account.Workspace, target string) error {
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

func (a *App) Environments(ctx context.Context, user string) ([]account.Environment, error) {
	var envs []account.Environment
	err := a.st.Read(ctx, func(r store.Reader) (err error) { envs, err = r.Environments(user); return err })
	return envs, err
}

func (a *App) AddEnvironment(ctx context.Context, user, name string, kind account.EnvironmentKind) (account.Environment, error) {
	name, err := account.Name("an environment name", name)
	if err != nil {
		return account.Environment{}, err
	}
	if kind, err = account.NewEnvironmentKind(kind); err != nil {
		return account.Environment{}, err
	}
	now := time.Now().UTC()
	e := account.Environment{ID: uuid.NewString(), Name: name, Kind: kind, Created: now, Updated: now}
	err = a.st.Update(ctx, func(tx store.Tx) error { return a.putEnvironment(tx, user, e) })
	return e, err
}

func (a *App) RenameEnvironment(ctx context.Context, user, id, name string) (account.Environment, error) {
	name, err := account.Name("an environment name", name)
	if err != nil {
		return account.Environment{}, err
	}
	return edit(ctx, a.st,
		func(tx store.Tx) (account.Environment, error) { return tx.Environment(user, id) },
		func(_ store.Tx, e *account.Environment) error {
			e.Name, e.Updated = name, time.Now().UTC()
			return nil
		},
		func(tx store.Tx, e account.Environment) error { return a.putEnvironment(tx, user, e) })
}

func (a *App) putEnvironment(tx store.Tx, user string, e account.Environment) error {
	envs, err := tx.Environments(user)
	if err != nil {
		return err
	}
	for _, other := range envs {
		if other.ID != e.ID && strings.EqualFold(other.Name, e.Name) {
			return fmt.Errorf("%w: you already have an environment called %q", account.ErrInvalidName, other.Name)
		}
	}
	return tx.PutEnvironment(user, e)
}

func (a *App) DeleteEnvironment(ctx context.Context, user, id string) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		e, err := tx.Environment(user, id)
		if err != nil {
			return err
		}
		if e.Kind == account.Local {
			return fmt.Errorf("%w: %s is this machine, whose folders the workspaces are", account.ErrInvalidName, e.Name)
		}
		used, err := tx.EnvironmentInUse(user, id)
		if err != nil {
			return err
		}
		if used {
			return fmt.Errorf("environment %s: %w", e.Name, ErrEnvironmentInUse)
		}
		return tx.DeleteEnvironment(user, id)
	})
}

type MCPServer struct {
	account.MCPServer
	Status *account.MCPStatus
}

func (a *App) MCPServers(ctx context.Context, user string) ([]MCPServer, error) {
	var stored []account.MCPServer
	if err := a.st.Read(ctx, func(r store.Reader) (err error) { stored, err = r.MCPServers(user); return err }); err != nil {
		return nil, err
	}
	statuses, err := a.statuses(user)
	if err != nil {
		return nil, err
	}
	out := make([]MCPServer, len(stored))
	for i, s := range stored {
		out[i].MCPServer = s
		ofServer := func(st account.MCPStatus) bool { return st.Name == s.Name }
		if j := slices.IndexFunc(statuses, ofServer); j >= 0 {
			out[i].Status = &statuses[j]
		}
	}
	return out, nil
}

func (a *App) SetMCPServer(ctx context.Context, user string, s account.MCPServer) error {
	box, err := a.toolbox(ctx, user)
	if err != nil {
		return err
	}
	return a.st.Update(ctx, func(tx store.Tx) error {
		mine, err := tx.MCPServers(user)
		if err != nil {
			return err
		}
		var providers []string
		for _, t := range box.Tools {
			mineToo := slices.ContainsFunc(mine, func(m account.MCPServer) bool { return m.Name == t.Provider })
			if !mineToo && !slices.Contains(providers, t.Provider) {
				providers = append(providers, t.Provider)
			}
		}
		if err := s.Check(providers); err != nil {
			return err
		}
		return tx.PutMCPServer(user, s)
	})
}

func (a *App) DeleteMCPServer(ctx context.Context, user, name string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.DeleteMCPServer(user, name) })
}

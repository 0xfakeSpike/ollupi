package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/environment"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

const workspaceTimeout = time.Minute

func (a *App) Workspaces(ctx context.Context, user string) (ws []account.Workspace, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if ws, err = r.Workspaces(user); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return ws, seq, err
}

// AddWorkspace makes a folder of one of the user's environments a
// workspace; an empty environment is the user's first.
func (a *App) AddWorkspace(ctx context.Context, user, environment, path, name string) (account.Workspace, error) {
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	env, err := a.environment(ctx, user, environment)
	if err != nil {
		return account.Workspace{}, err
	}
	box, err := a.envs.In(ctx, user, env.ID)
	if err != nil {
		return account.Workspace{}, err
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
	existing, _, err := a.Workspaces(ctx, user)
	if err != nil {
		return account.Workspace{}, err
	}
	i := slices.IndexFunc(existing, func(w account.Workspace) bool { return w.Environment == env.ID && box.Same(ctx, w.Root, root) })
	now := time.Now().UTC()
	w := account.Workspace{ID: uuid.NewString(), Name: name, Environment: env.ID, Root: root, Kind: account.ProjectWorkspace, Created: now, Updated: now}
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
	var at account.Workspace
	if err := a.st.Read(ctx, func(r store.Reader) (err error) { at, err = r.Workspace(user, id); return err }); err != nil {
		return account.Workspace{}, err
	}
	box, err := a.envs.In(ctx, user, at.Environment)
	if err != nil {
		return account.Workspace{}, err
	}
	target, err := box.Directory(ctx, path)
	if err != nil {
		return account.Workspace{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	w, all, err := a.claimMove(ctx, user, id, target)
	if err != nil {
		return account.Workspace{}, err
	}
	from := w.Root
	stay := func(err error) (account.Workspace, error) {
		w, settled := a.settleMove(move, user, id, from)
		return w, errors.Join(err, settled)
	}
	if box.Same(ctx, from, target) {
		return stay(nil)
	}
	if err := checkTarget(ctx, box, w, all, target); err != nil {
		return stay(err)
	}
	if err := box.Move(move, from, target); err != nil {
		return stay(err)
	}
	moved, err := a.settleMove(move, user, id, target)
	if err == nil {
		return moved, nil
	}
	if back := box.Move(move, target, from); back != nil {
		return account.Workspace{}, fmt.Errorf("the folder is now at %s but the workspace still names %s: %w; moving it back failed too: %v", target, from, err, back)
	}
	return stay(err)
}

func (a *App) claimMove(ctx context.Context, user, id, target string) (w account.Workspace, all []account.Workspace, err error) {
	err = a.st.Update(ctx, func(tx store.Tx) error {
		if w, err = tx.Workspace(user, id); err != nil {
			return err
		}
		if all, err = tx.Workspaces(user); err != nil {
			return err
		}
		busy, err := tx.WorkspaceBusy(user, id)
		if err != nil {
			return err
		}
		if busy || w.MovingTo != "" {
			return fmt.Errorf("workspace %s: %w", w.Name, ErrWorkspaceBusy)
		}
		w.MovingTo, w.Updated = target, time.Now().UTC()
		return tx.PutWorkspace(user, w)
	})
	return w, all, err
}

func (a *App) settleMove(ctx context.Context, user, id, root string) (account.Workspace, error) {
	return a.changeWorkspace(ctx, user, id, func(_ store.Tx, w *account.Workspace) error {
		w.Root, w.MovingTo = root, ""
		return nil
	})
}

func (a *App) SettleMoves(ctx context.Context) error {
	var moves []store.Move
	if err := a.st.Read(ctx, func(r store.Reader) (err error) { moves, err = r.Moves(); return err }); err != nil {
		return fmt.Errorf("the workspaces left moving: %w", err)
	}
	for _, m := range moves {
		w := m.Workspace
		root, err := a.landed(ctx, m.User, w)
		if err == nil {
			_, err = a.settleMove(ctx, m.User, w.ID, root)
		}
		if err != nil {
			slog.Warn("app: a workspace left moving is not settled; it stays busy", "user", m.User, "workspace", w.ID, "error", err)
		}
	}
	return nil
}

func (a *App) landed(ctx context.Context, user string, w account.Workspace) (string, error) {
	box, err := a.envs.In(ctx, user, w.Environment)
	if err != nil {
		return "", err
	}
	if _, err := box.Stat(ctx, w.Root); err == nil {
		return w.Root, nil
	}
	if _, err := box.Stat(ctx, w.MovingTo); err == nil {
		return w.MovingTo, nil
	}
	return w.Root, nil
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
		if other.ID != w.ID && other.Environment == w.Environment && box.Same(ctx, other.Root, target) {
			return fmt.Errorf("%w: %q is the folder of the workspace %s", ErrInvalidPath, target, other.Name)
		}
	}
	return nil
}

func (a *App) Environments(ctx context.Context, user string) (envs []account.Environment, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if envs, err = r.Environments(user); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return envs, seq, err
}

// environment is one of the user's environments; an empty id is their
// first.
func (a *App) environment(ctx context.Context, user, id string) (env account.Environment, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error { env, err = environmentOf(r, user, id); return err })
	return env, err
}

func environmentOf(r store.Reader, user, id string) (account.Environment, error) {
	if id != "" {
		return r.Environment(user, id)
	}
	envs, err := r.Environments(user)
	if err != nil {
		return account.Environment{}, err
	}
	if len(envs) == 0 {
		return account.Environment{}, fmt.Errorf("user %s has no environment", user)
	}
	return envs[0], nil
}

// Providers are the sandboxes the deployment offers an environment of.
func (a *App) Providers() []environment.Provider { return a.envs.Providers() }

// AddEnvironment names a new sandbox of one of the deployment's providers,
// with config. It is made the first time a conversation runs in it.
func (a *App) AddEnvironment(ctx context.Context, user, name, provider string, config json.RawMessage) (account.Environment, error) {
	name, err := account.Name("an environment name", name)
	if err != nil {
		return account.Environment{}, err
	}
	if err := a.envs.Check(provider, config); err != nil {
		return account.Environment{}, fmt.Errorf("%w: %v", ErrInvalidEnvironment, err)
	}
	now := time.Now().UTC()
	e := account.NewEnvironment(name, provider, config)
	e.Created, e.Updated = now, now
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

// DeleteEnvironment deletes an environment no conversation or workspace is
// in, then removes its sandbox. A user's first environment stays.
func (a *App) DeleteEnvironment(ctx context.Context, user, id string) error {
	var e account.Environment
	err := a.st.Update(ctx, func(tx store.Tx) (err error) {
		if e, err = tx.Environment(user, id); err != nil {
			return err
		}
		first, err := environmentOf(tx, user, "")
		if err != nil {
			return err
		}
		if first.ID == e.ID {
			return fmt.Errorf("environment %s: %w", e.Name, ErrFirstEnvironment)
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
	if err != nil {
		return err
	}
	if err := a.envs.Remove(context.WithoutCancel(ctx), user, e); err != nil {
		return fmt.Errorf("the environment is deleted, but its sandbox may be left: %w", err)
	}
	return nil
}

type MCPServer struct {
	account.MCPServer
	Status *account.MCPStatus
}

func (a *App) MCPServers(ctx context.Context, user string) ([]MCPServer, int64, error) {
	var stored []account.MCPServer
	var seq int64
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		if stored, err = r.MCPServers(user); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	statuses, err := a.statuses(user)
	if err != nil {
		return nil, 0, err
	}
	out := make([]MCPServer, len(stored))
	for i, s := range stored {
		out[i].MCPServer = s
		ofServer := func(st account.MCPStatus) bool { return st.Name == s.Name }
		if j := slices.IndexFunc(statuses, ofServer); j >= 0 {
			out[i].Status = &statuses[j]
		}
	}
	return out, seq, nil
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

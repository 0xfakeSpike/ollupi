package app

import (
	"context"
	"time"

	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
)

func (a *App) Settings(ctx context.Context, u string) (user.Settings, error) {
	var s user.Settings
	err := a.st.Read(ctx, func(r store.Reader) (err error) { s, err = r.Settings(u); return err })
	return s, err
}

// UpdateSettings sets redaction and memory; one left nil stays as it is.
func (a *App) UpdateSettings(ctx context.Context, u string, redaction, memory *bool) (user.Settings, error) {
	var s user.Settings
	err := a.st.Update(ctx, func(tx store.Tx) (err error) {
		if s, err = tx.Settings(u); err != nil {
			return err
		}
		if redaction != nil {
			s.Redaction = *redaction
		}
		if memory != nil {
			s.Memory = *memory
		}
		return tx.SetSettings(u, s)
	})
	return s, err
}

// Each layer of permissions is checked on the layers beneath it, as they are when it is written.

func (a *App) UpdateUserPermissions(ctx context.Context, u string, p policy.Settings) (user.Settings, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return user.Settings{}, err
	}
	var s user.Settings
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		if s, err = tx.Settings(u); err != nil {
			return err
		}
		next := s
		next.Permissions = p
		if err := policy.Validate([]policy.Layer{user.Deployment(a.deployment)}, s.Permissions, next.Layer(), box); err != nil {
			return err
		}
		s = next
		return tx.SetSettings(u, s)
	})
	return s, err
}

func (a *App) UpdateWorkspacePermissions(ctx context.Context, u, id string, p policy.Settings) (user.Workspace, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return user.Workspace{}, err
	}
	var w user.Workspace
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		if w, err = tx.Workspace(u, id); err != nil {
			return err
		}
		return a.setWorkspacePermissions(tx, box, u, &w, func(policy.Settings) policy.Settings { return p })
	})
	return w, err
}

func (a *App) setWorkspacePermissions(tx store.Tx, box policy.Toolbox, u string, w *user.Workspace, change func(policy.Settings) policy.Settings) error {
	s, err := tx.Settings(u)
	if err != nil {
		return err
	}
	next := *w
	next.Settings = change(w.Settings)
	if err := policy.Validate([]policy.Layer{user.Deployment(a.deployment), s.Layer()}, w.Settings, next.Layer(), box); err != nil {
		return err
	}
	next.Updated = time.Now().UTC()
	*w = next
	return tx.PutWorkspace(u, next)
}

func (a *App) UpdateThreadPermissions(ctx context.Context, u, id string, p policy.Settings) (store.Thread, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return store.Thread{}, err
	}
	var th store.Thread
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		if th, err = topThread(tx, u, id); err != nil {
			return err
		}
		return a.setThreadPermissions(tx, box, &th, func(policy.Settings) policy.Settings { return p })
	})
	return th, err
}

func (a *App) setThreadPermissions(tx store.Tx, box policy.Toolbox, th *store.Thread, change func(policy.Settings) policy.Settings) error {
	s, err := tx.Settings(th.User)
	if err != nil {
		return err
	}
	var w *user.Workspace
	if th.Workspace != "" {
		ws, err := tx.Workspace(th.User, th.Workspace)
		if err != nil {
			return err
		}
		w = &ws
	}
	next := *th
	next.Settings = change(th.Settings)
	base := user.Layers(user.Deployment(a.deployment), s, w, next.Layer())
	if err := policy.Validate(base[:len(base)-1], th.Settings, next.Layer(), box); err != nil {
		return err
	}
	next.Updated = time.Now().UTC()
	*th = next
	return tx.UpdateThread(next)
}

// allowAlways keeps rules where the conversation's permissions come from: its workspace, or else the conversation
// itself; a subagent's, its parent's.
func (a *App) allowAlways(tx store.Tx, box policy.Toolbox, id string, rules []string) error {
	th, err := tx.Thread(id)
	if err != nil {
		return err
	}
	if th.Parent != "" {
		if th, err = tx.Thread(th.Parent); err != nil {
			return err
		}
	}
	widen := func(s policy.Settings) policy.Settings {
		return policy.Resolve(s, policy.Settings{Rules: policy.Rules{Allow: rules}})
	}
	if th.Workspace == "" {
		return a.setThreadPermissions(tx, box, &th, widen)
	}
	w, err := tx.Workspace(th.User, th.Workspace)
	if err != nil {
		return err
	}
	return a.setWorkspacePermissions(tx, box, th.User, &w, widen)
}

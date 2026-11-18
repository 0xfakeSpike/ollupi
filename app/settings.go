package app

import (
	"context"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) Settings(ctx context.Context, user string) (account.Settings, error) {
	var s account.Settings
	err := a.st.Read(ctx, func(r store.Reader) (err error) { s, err = r.Settings(user); return err })
	return s, err
}

func (a *App) changeSettings(ctx context.Context, user string, change func(*account.Settings) error) (account.Settings, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (account.Settings, error) { return tx.Settings(user) },
		func(_ store.Tx, s *account.Settings) error { return change(s) },
		func(tx store.Tx, s account.Settings) error { return tx.SetSettings(user, s) })
}

func (a *App) UpdateSettings(ctx context.Context, user string, redaction, memory *bool) (account.Settings, error) {
	return a.changeSettings(ctx, user, func(s *account.Settings) error {
		if redaction != nil {
			s.Redaction = *redaction
		}
		if memory != nil {
			s.Memory = *memory
		}
		return nil
	})
}

func (a *App) UpdateUserPermissions(ctx context.Context, user string, p policy.Settings) (account.Settings, error) {
	box, err := a.toolbox(ctx, user)
	if err != nil {
		return account.Settings{}, err
	}
	return a.changeSettings(ctx, user, func(s *account.Settings) error {
		next := *s
		next.Permissions = p
		if err := policy.Validate([]policy.Layer{account.Deployment(a.deployment)}, s.Permissions, next.Layer(), box); err != nil {
			return err
		}
		*s = next
		return nil
	})
}

func (a *App) UpdateWorkspacePermissions(ctx context.Context, user, id string, p policy.Settings) (account.Workspace, error) {
	box, err := a.toolbox(ctx, user)
	if err != nil {
		return account.Workspace{}, err
	}
	return a.changeWorkspace(ctx, user, id, func(tx store.Tx, w *account.Workspace) error {
		return a.permitWorkspace(tx, box, user, w, p)
	})
}

func (a *App) permitWorkspace(tx store.Tx, box policy.Toolbox, user string, w *account.Workspace, next policy.Settings) error {
	s, err := tx.Settings(user)
	if err != nil {
		return err
	}
	changed := *w
	changed.Settings = next
	if err := policy.Validate([]policy.Layer{account.Deployment(a.deployment), s.Layer()}, w.Settings, changed.Layer(), box); err != nil {
		return err
	}
	w.Settings = next
	return nil
}

func (a *App) UpdateThreadPermissions(ctx context.Context, user, id string, p policy.Settings) (store.Thread, error) {
	box, err := a.toolbox(ctx, user)
	if err != nil {
		return store.Thread{}, err
	}
	return a.changeThread(ctx, user, id, func(tx store.Tx, th *store.Thread) error { return a.permitThread(tx, box, th, p) })
}

func (a *App) permitThread(tx store.Tx, box policy.Toolbox, th *store.Thread, next policy.Settings) error {
	s, err := tx.Settings(th.User)
	if err != nil {
		return err
	}
	below := []policy.Layer{account.Deployment(a.deployment), s.Layer()}
	if th.Workspace != "" {
		w, err := tx.Workspace(th.User, th.Workspace)
		if err != nil {
			return err
		}
		below = append(below, w.Layer())
	}
	changed := *th
	changed.Settings = next
	if err := policy.Validate(below, th.Settings, changed.Layer(), box); err != nil {
		return err
	}
	th.Settings = next
	return nil
}

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
	allowed := policy.Settings{Rules: policy.Rules{Allow: rules}}
	now := time.Now().UTC()
	if th.Workspace == "" {
		if err := a.permitThread(tx, box, &th, policy.Resolve(th.Settings, allowed)); err != nil {
			return err
		}
		th.Updated = now
		return tx.UpdateThread(th)
	}
	w, err := tx.Workspace(th.User, th.Workspace)
	if err != nil {
		return err
	}
	if err := a.permitWorkspace(tx, box, th.User, &w, policy.Resolve(w.Settings, allowed)); err != nil {
		return err
	}
	w.Updated = now
	return tx.PutWorkspace(th.User, w)
}

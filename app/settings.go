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

func (a *App) changeSettings(ctx context.Context, u string, change func(*user.Settings) error) (user.Settings, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (user.Settings, error) { return tx.Settings(u) },
		func(_ store.Tx, s *user.Settings) error { return change(s) },
		func(tx store.Tx, s user.Settings) error { return tx.SetSettings(u, s) })
}

func (a *App) UpdateSettings(ctx context.Context, u string, redaction, memory *bool) (user.Settings, error) {
	return a.changeSettings(ctx, u, func(s *user.Settings) error {
		if redaction != nil {
			s.Redaction = *redaction
		}
		if memory != nil {
			s.Memory = *memory
		}
		return nil
	})
}

func (a *App) UpdateUserPermissions(ctx context.Context, u string, p policy.Settings) (user.Settings, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return user.Settings{}, err
	}
	return a.changeSettings(ctx, u, func(s *user.Settings) error {
		next := *s
		next.Permissions = p
		if err := policy.Validate([]policy.Layer{user.Deployment(a.deployment)}, s.Permissions, next.Layer(), box); err != nil {
			return err
		}
		*s = next
		return nil
	})
}

func (a *App) UpdateWorkspacePermissions(ctx context.Context, u, id string, p policy.Settings) (user.Workspace, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return user.Workspace{}, err
	}
	return a.changeWorkspace(ctx, u, id, func(tx store.Tx, w *user.Workspace) error {
		return a.permitWorkspace(tx, box, u, w, p)
	})
}

func (a *App) permitWorkspace(tx store.Tx, box policy.Toolbox, u string, w *user.Workspace, next policy.Settings) error {
	s, err := tx.Settings(u)
	if err != nil {
		return err
	}
	changed := *w
	changed.Settings = next
	if err := policy.Validate([]policy.Layer{user.Deployment(a.deployment), s.Layer()}, w.Settings, changed.Layer(), box); err != nil {
		return err
	}
	w.Settings = next
	return nil
}

func (a *App) UpdateThreadPermissions(ctx context.Context, u, id string, p policy.Settings) (store.Thread, error) {
	box, err := a.toolbox(ctx, u)
	if err != nil {
		return store.Thread{}, err
	}
	return a.changeThread(ctx, u, id, func(tx store.Tx, th *store.Thread) error { return a.permitThread(tx, box, th, p) })
}

func (a *App) permitThread(tx store.Tx, box policy.Toolbox, th *store.Thread, next policy.Settings) error {
	s, err := tx.Settings(th.User)
	if err != nil {
		return err
	}
	below := []policy.Layer{user.Deployment(a.deployment), s.Layer()}
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

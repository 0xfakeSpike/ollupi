package app

import (
	"context"
	"slices"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

// Users lists the profiles of this home, oldest first.
func (a *App) Users(ctx context.Context) ([]account.User, error) {
	var out []account.User
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		out, err = r.Profiles()
		return err
	})
	return out, err
}

// CreateUser makes a profile with its own name and colour. Its data starts
// empty: its own settings, its own environment, and the hub its first
// conversation makes.
func (a *App) CreateUser(ctx context.Context, name, color string) (account.User, error) {
	name, err := account.Name("a profile name", name)
	if err != nil {
		return account.User{}, err
	}
	color, err = account.ProfileColor(color)
	if err != nil {
		return account.User{}, err
	}
	id := uuid.NewString()
	err = a.st.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser(id); err != nil {
			return err
		}
		if err := tx.SetUserName(id, name); err != nil {
			return err
		}
		return tx.SetUserColor(id, color)
	})
	if err != nil {
		return account.User{}, err
	}
	return a.user(ctx, id)
}

// RenameUser names a profile again.
func (a *App) RenameUser(ctx context.Context, id, name string) (account.User, error) {
	name, err := account.Name("a profile name", name)
	if err != nil {
		return account.User{}, err
	}
	if err := a.st.Update(ctx, func(tx store.Tx) error { return tx.SetUserName(id, name) }); err != nil {
		return account.User{}, err
	}
	return a.user(ctx, id)
}

// SetUserColor gives a profile another colour.
func (a *App) SetUserColor(ctx context.Context, id, color string) (account.User, error) {
	color, err := account.ProfileColor(color)
	if err != nil {
		return account.User{}, err
	}
	if err := a.st.Update(ctx, func(tx store.Tx) error { return tx.SetUserColor(id, color) }); err != nil {
		return account.User{}, err
	}
	return a.user(ctx, id)
}

func (a *App) user(ctx context.Context, id string) (account.User, error) {
	var out account.User
	err := a.st.Read(ctx, func(r store.Reader) error {
		users, err := r.Profiles()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(users, func(u account.User) bool { return u.ID == id })
		if i < 0 {
			return store.ErrNotFound
		}
		out = users[i]
		return nil
	})
	return out, err
}

package app

import (
	"context"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

// Users are every user of the deployment with their names, and the commit
// they were read at. It is the operator's to read: a user is told only of
// themself (User).
func (a *App) Users(ctx context.Context) (users []account.User, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if users, err = r.UserList(); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return users, seq, err
}

func (a *App) User(ctx context.Context, id string) (u account.User, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error { u, err = r.User(id); return err })
	return u, err
}

// CreateUser makes a user, named, with a new id: their settings, their
// first environment, and nothing else of anyone's.
func (a *App) CreateUser(ctx context.Context, name string) (account.User, error) {
	name, err := account.UserName(name)
	if err != nil {
		return account.User{}, err
	}
	u := account.User{ID: uuid.NewString(), Name: name}
	return u, a.st.Update(ctx, func(tx store.Tx) error { return tx.CreateUser(u, a.envs.First()) })
}

func (a *App) RenameUser(ctx context.Context, id, name string) (account.User, error) {
	name, err := account.UserName(name)
	if err != nil {
		return account.User{}, err
	}
	return account.User{ID: id, Name: name}, a.st.Update(ctx, func(tx store.Tx) error { return tx.SetUserName(id, name) })
}

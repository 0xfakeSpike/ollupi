package sqlite

import (
	"errors"
	"testing"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestAUserIsMadeNamedAndRenamedAndEveryoneHearsIt(t *testing.T) {
	s := open(t, t.TempDir())
	update(t, s, func(tx store.Tx) error {
		return tx.EnsureUser("old", account.NewEnvironment("This computer", "local", nil))
	})
	var heard []account.User
	s.Feed().Subscribe("old", func(e store.Event) {
		if e.Owned != nil && e.Owned.User != nil && e.User == "" {
			heard = append(heard, *e.Owned.User)
		}
	})
	update(t, s, func(tx store.Tx) error {
		return tx.CreateUser(account.User{ID: "new", Name: "Work"}, account.NewEnvironment("This computer", "local", nil))
	})
	if err := s.Update(t.Context(), func(tx store.Tx) error {
		return tx.CreateUser(account.User{ID: "new", Name: "Again"}, account.NewEnvironment("This computer", "local", nil))
	}); err == nil {
		t.Fatal("a user was made twice")
	}
	update(t, s, func(tx store.Tx) error { return tx.SetUserName("old", "Me") })
	if err := s.Update(t.Context(), func(tx store.Tx) error { return tx.SetUserName("nobody", "x") }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renamed a user who is not there: %v", err)
	}
	if len(heard) != 2 || heard[0] != (account.User{ID: "new", Name: "Work"}) || heard[1] != (account.User{ID: "old", Name: "Me"}) {
		t.Fatalf("heard %+v", heard)
	}
	list := read(t, s, func(r store.Reader) ([]account.User, error) { return r.UserList() })
	if len(list) != 2 || list[0] != (account.User{ID: "new", Name: "Work"}) || list[1] != (account.User{ID: "old", Name: "Me"}) {
		t.Fatalf("users %+v", list)
	}
	// A user made is a user like any other: their settings and this machine.
	if envs := read(t, s, func(r store.Reader) ([]account.Environment, error) { return r.Environments("new") }); len(envs) != 1 {
		t.Fatalf("new's environments %+v", envs)
	}
}

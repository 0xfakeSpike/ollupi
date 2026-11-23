package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestAProfileKeepsItsNameAndColour(t *testing.T) {
	s := openFor(t, t.TempDir(), "")
	ctx := context.Background()
	err := s.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		if err := tx.SetUserName("u", "我"); err != nil {
			return err
		}
		return tx.SetUserColor("u", "teal")
	})
	if err != nil {
		t.Fatal(err)
	}
	var users []account.User
	if err := s.Read(ctx, func(r store.Reader) (err error) {
		users, err = r.Profiles()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != "u" || users[0].Name != "我" || users[0].Color != "teal" {
		t.Fatalf("profiles = %+v", users)
	}
	if err := s.Update(ctx, func(tx store.Tx) error { return tx.SetUserName("nobody", "x") }); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renaming an unknown profile = %v", err)
	}
}

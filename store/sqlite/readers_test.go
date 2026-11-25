package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestReadsGoOnWhileAWriteIsOpen(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	if err := s.Update(ctx, func(tx store.Tx) error {
		return tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil))
	}); err != nil {
		t.Fatal(err)
	}
	writing, release := make(chan struct{}), make(chan struct{})
	written := make(chan error, 1)
	go func() {
		written <- s.Update(ctx, func(tx store.Tx) error {
			if err := tx.SetSettings("u", account.Settings{Model: "written"}); err != nil {
				return err
			}
			close(writing)
			<-release
			return nil
		})
	}()
	<-writing
	read := make(chan account.Settings, 1)
	go func() {
		s.Read(ctx, func(r store.Reader) error {
			got, err := r.Settings("u")
			if err != nil {
				t.Error(err)
			}
			read <- got
			return err
		})
	}()
	select {
	case got := <-read:
		if got.Model == "written" {
			t.Error("a read saw a write that was not committed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read waited for an open write")
	}
	close(release)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(r store.Reader) error {
		got, err := r.Settings("u")
		if err == nil && got.Model != "written" {
			t.Error("a read after the commit missed it")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

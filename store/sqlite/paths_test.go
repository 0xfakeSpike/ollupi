package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestStoreRoot(t *testing.T) {
	cases := []struct{ root, base, stored string }{
		{"/Users/one/Desktop/app", "/Users/one", "~/Desktop/app"},
		{"/Users/one", "/Users/one", "~"},
		{"/Users/one/Desktop/app", "", "/Users/one/Desktop/app"},
		{"/Users/one/Desktop/app", "/Users/two", "/Users/one/Desktop/app"},
		{"/Volumes/work/app", "/Users/one", "/Volumes/work/app"},
		{"/Users/onex/app", "/Users/one", "/Users/onex/app"},
	}
	for _, c := range cases {
		if got := storeRoot(c.root, c.base); got != c.stored {
			t.Errorf("storeRoot(%q, %q) = %q, want %q", c.root, c.base, got, c.stored)
		}
		if got := readRoot(c.stored, c.base); got != filepath.Clean(c.root) {
			t.Errorf("readRoot(%q, %q) = %q, want %q", c.stored, c.base, got, c.root)
		}
	}
}

// The same store, opened on a machine whose user is someone else: the rows
// still name their folders, now under that user's home.
func TestWorkspaceRootsTravel(t *testing.T) {
	home := t.TempDir()
	s := openFor(t, home, "/Users/one")
	putRoot(t, s, "w1", "/Users/one/Desktop/app")
	s.Close()

	moved := openFor(t, home, "/Users/two")
	if root := rootOf(t, moved, "w1"); root != "/Users/two/Desktop/app" {
		t.Errorf("root = %q, want %q", root, "/Users/two/Desktop/app")
	}
}

// A store written before roots were kept relative carries over too: opening
// it here rewrites each row once.
func TestAbsoluteRootsBecomeRelative(t *testing.T) {
	home := t.TempDir()
	legacy := openFor(t, home, "")
	putRoot(t, legacy, "w1", "/Users/one/Desktop/app")
	legacy.Close()

	s := openFor(t, home, "/Users/one")
	if root := rootOf(t, s, "w1"); root != "/Users/one/Desktop/app" {
		t.Errorf("root = %q, want %q", root, "/Users/one/Desktop/app")
	}
	var stored string
	if err := s.g.Raw("SELECT root FROM workspaces WHERE id = 'w1'").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != "~/Desktop/app" {
		t.Errorf("stored = %q, want %q", stored, "~/Desktop/app")
	}
}

func openFor(t *testing.T, home, base string) *Store {
	t.Helper()
	s, err := openAt(home, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putRoot(t *testing.T, s *Store, id, root string) {
	t.Helper()
	now := time.Now().UTC()
	err := s.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.EnsureUser("a"); err != nil {
			return err
		}
		return tx.PutWorkspace("a", account.Workspace{ID: id, Name: "app", Root: root, Created: now, Updated: now})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rootOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var root string
	err := s.Read(context.Background(), func(r store.Reader) error {
		w, err := r.Workspace("a", id)
		root = w.Root
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

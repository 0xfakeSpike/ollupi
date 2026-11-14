package blobs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := Dir(root)
	for _, key := range []string{"thread/a.txt", "thread/b.txt", "other/c.txt"} {
		if err := s.Put(ctx, key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Get(ctx, "thread/a.txt"); err != nil || string(got) != "thread/a.txt" {
		t.Errorf("get: %q, %v", got, err)
	}
	if err := s.Put(ctx, "thread/a.txt", []byte("again")); err == nil {
		t.Error("a key was written twice")
	}
	if st, err := os.Stat(filepath.Join(root, "thread", "a.txt")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file: %v, %v", st, err)
	}
	if err := s.Delete(ctx, "thread/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "thread/a.txt"); err != nil {
		t.Errorf("deleting what is gone: %v", err)
	}
	if _, err := s.Get(ctx, "thread/a.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a deleted object: %v", err)
	}
	if err := s.DeletePrefix(ctx, "thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "thread/b.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an object under the prefix: %v", err)
	}
	if _, err := s.Get(ctx, "other/c.txt"); err != nil {
		t.Errorf("an object elsewhere: %v", err)
	}
	if err := s.Put(ctx, "../out.txt", nil); err == nil {
		t.Error("a key left the store")
	}
}

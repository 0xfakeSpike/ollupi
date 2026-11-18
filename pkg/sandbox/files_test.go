//go:build unix

package sandbox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func tree(t *testing.T) (s *Sandbox, root, secret string) {
	t.Helper()
	base, err := canonicalPath(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	root, secret = filepath.Join(base, "project"), filepath.Join(base, "secret")
	for _, dir := range []string{filepath.Join(root, "sub"), secret} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{filepath.Join(root, "sub", "note.txt"): "note", filepath.Join(secret, "note.txt"): "key"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Sandbox{policy: policy{Mode: ModeWorkspaceWrite, WritableRoots: []string{root}, DenyRead: []string{secret}}}, root, secret
}

func swap(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
}

func TestALinkPutOnTheWayDoesNotOpenAClosedFile(t *testing.T) {
	s, root, secret := tree(t)
	path := filepath.Join(root, "sub", "note.txt")
	if err := s.CheckRead(path); err != nil {
		t.Fatal(err)
	}
	swap(t, filepath.Join(root, "sub"), secret)
	if f, err := s.Open(context.Background(), path); err == nil {
		body, _ := io.ReadAll(f)
		f.Close()
		t.Fatalf("read %q through the link", body)
	}
}

func TestOpenFollowsALinkToAnOpenFile(t *testing.T) {
	s, root, _ := tree(t)
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(root, "sub", "note.txt"), link); err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if body, _ := io.ReadAll(f); string(body) != "note" {
		t.Errorf("read %q, want the note", body)
	}
}

func TestOpenRefusesAFIFOAtOnce(t *testing.T) {
	s, root, _ := tree(t)
	fifo := filepath.Join(root, "pipe")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	go func() {
		f, err := s.Open(context.Background(), fifo)
		if err == nil {
			f.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err == nil {
			t.Error("a FIFO opens as a file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO waits for a writer")
	}
}

func TestALinkPutOnTheWayIsNotWrittenThrough(t *testing.T) {
	s, root, secret := tree(t)
	path := filepath.Join(root, "sub", "authorized_keys")
	if err := s.CheckWrite([]string{root}, path); err != nil {
		t.Fatal(err)
	}
	swap(t, filepath.Join(root, "sub"), secret)
	if err := s.WriteFile(context.Background(), path, []byte("key"), 0o644); err == nil {
		t.Error("the write went through the link")
	}
	if _, err := os.Lstat(filepath.Join(secret, "authorized_keys")); err == nil {
		t.Error("the file was written where the link leads")
	}
}

func TestWriteFileMakesTheFoldersAboveIt(t *testing.T) {
	s, root, _ := tree(t)
	path := filepath.Join(root, "a", "b", "c.txt")
	if err := s.WriteFile(context.Background(), path, []byte("c"), 0o640); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640", st.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("the folder holds %v (%v), want only the file", entries, err)
	}
}

func TestReadFileRefusesWhatHasNoEnd(t *testing.T) {
	s, root, _ := tree(t)
	ctx := context.Background()
	zero := filepath.Join(root, ".gitignore")
	if err := os.Symlink("/dev/zero", zero); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(ctx, s, zero, 1<<20); err == nil {
		t.Error("a link to a device reads as a file")
	}
	big := filepath.Join(root, "big")
	if err := os.WriteFile(big, make([]byte, 1<<10+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(ctx, s, big, 1<<10); err == nil {
		t.Error("a file over the limit is read")
	}
	if body, err := ReadFile(ctx, s, filepath.Join(root, "sub", "note.txt"), 4); err != nil || string(body) != "note" {
		t.Errorf("a file at the limit: %q, %v", body, err)
	}
}

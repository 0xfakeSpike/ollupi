package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAFullAccessCommandStillReadsNothingWithheld(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only Seatbelt keeps a command with full access from a folder")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	data, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(data, "store"), []byte("keys"), 0o600)
	os.WriteFile(filepath.Join(root, "notes"), []byte("mine"), 0o600)
	s, err := New(Config{Mode: "workspace-write", Roots: []string{root}, DenyRead: []string{data}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := func(path string) Result {
		res, err := s.Run(context.Background(), Command{Program: "/bin/cat", Args: []string{path}, Cwd: root, FullAccess: true, Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(filepath.Join(data, "store")); res.ExitCode == 0 || strings.Contains(res.Stdout, "keys") {
		t.Errorf("a command with full access read the withheld folder: %+v", res)
	}
	if res := run(filepath.Join(root, "notes")); res.ExitCode != 0 || res.Stdout != "mine" {
		t.Errorf("a command with full access could not read the workspace: %+v", res)
	}
	home, _ := os.UserHomeDir()
	if res, _ := s.Run(context.Background(), Command{Program: "/bin/ls", Args: []string{home}, FullAccess: true, Timeout: 10 * time.Second}); res.ExitCode != 0 {
		t.Errorf("a command with full access could not list the home: %+v", res)
	}
}

func TestMakeDirMakesOneFolderAndNoneWithheld(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	data, _ := filepath.EvalSymlinks(t.TempDir())
	s, err := New(Config{Mode: "full-access", Roots: []string{root}, DenyRead: []string{data}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.MakeDir(ctx, filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(root, "new")); err != nil || !st.IsDir() {
		t.Errorf("the folder made: %v", err)
	}
	if err := s.MakeDir(ctx, filepath.Join(root, "a", "b")); err == nil {
		t.Error("a folder was made with its parent missing")
	}
	if err := s.MakeDir(ctx, filepath.Join(data, "inside")); err == nil {
		t.Error("a folder was made in a withheld one")
	}
}

func TestTheFreedesktopTrashRecordsWhereAFileWas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	dir := t.TempDir()
	for range 2 {
		path := filepath.Join(dir, "a file.txt")
		os.WriteFile(path, []byte("x"), 0o600)
		if err := freedesktopTrash(path, home); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("still there: %v", err)
		}
	}
	trash := filepath.Join(home, ".local", "share", "Trash")
	for _, name := range []string{"a file.txt", "a file.txt.2"} {
		if _, err := os.Stat(filepath.Join(trash, "files", name)); err != nil {
			t.Errorf("the trashed file %s: %v", name, err)
		}
		info, err := os.ReadFile(filepath.Join(trash, "info", name+".trashinfo"))
		if err != nil || !strings.Contains(string(info), "Path="+strings.ReplaceAll(filepath.Join(dir, "a file.txt"), " ", "%20")) {
			t.Errorf("the record of %s: %q, %v", name, info, err)
		}
	}
}

func TestAnEnvironmentKeepsAFolderBesideItsOwn(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	own := filepath.Join(dir, "scratch")
	os.Mkdir(own, 0o755)
	s, err := New(Config{Mode: "full-access", Roots: []string{own}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Folder(context.Background(), "super-agent")
	if err != nil || got != filepath.Join(dir, "super-agent") {
		t.Fatalf("the kept folder %q, %v", got, err)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Errorf("the kept folder was not made: %v", err)
	}
	if again, err := s.Folder(context.Background(), "super-agent"); err != nil || again != got {
		t.Errorf("asked again: %q, %v", again, err)
	}
	for _, name := range []string{"", "..", "a/b"} {
		if _, err := s.Folder(context.Background(), name); err == nil {
			t.Errorf("a folder kept for %q", name)
		}
	}
}

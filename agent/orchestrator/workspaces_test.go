package orchestrator_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
)

// A workspace moves with the files in it, and keeps its id and name: its threads and memories hang off those.
func TestAWorkspaceMovesWithTheFilesInIt(t *testing.T) {
	ctx := context.Background()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Without BoxFor the agent works on this machine's files, as the desktop's backend does.
	ag := orchestrator.New(orchestrator.Config{DB: db})

	root := canonical(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := ag.AddWorkspace(ctx, repo.DefaultUser, root, "app")
	if err != nil {
		t.Fatal(err)
	}
	target := canonical(t, t.TempDir())

	moved, err := ag.MoveWorkspace(ctx, repo.DefaultUser, w.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != w.ID || moved.Name != "app" || moved.RootPath != target {
		t.Errorf("moved to %+v, want id %s named app at %s", moved, w.ID, target)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the old folder is still there: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(target, "notes.txt")); err != nil || string(body) != "kept\n" {
		t.Errorf("notes.txt holds %q (%v)", body, err)
	}
	if st, err := os.Stat(filepath.Join(target, "src", "deep")); err != nil || !st.IsDir() {
		t.Errorf("the folders under it did not come along: %v", err)
	}
	saved, err := db.WorkspaceOf(repo.DefaultUser, w.ID)
	if err != nil || saved.RootPath != target {
		t.Errorf("the database names %q (%v), want %s", saved.RootPath, err, target)
	}
	// The new folder is where the workspace's threads now run.
	if again, err := ag.MoveWorkspace(ctx, repo.DefaultUser, w.ID, target); err != nil || again.RootPath != target {
		t.Errorf("moving it onto itself: %+v, %v", again, err)
	}
}

// A move that would lose files or make the root ambiguous is refused, and the workspace stays where it was.
func TestAWorkspaceDoesNotMoveOntoAFolderThatIsTaken(t *testing.T) {
	ctx := context.Background()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ag := orchestrator.New(orchestrator.Config{DB: db})

	root := canonical(t, t.TempDir())
	w, err := ag.AddWorkspace(ctx, repo.DefaultUser, root, "app")
	if err != nil {
		t.Fatal(err)
	}
	full := canonical(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(full, "theirs.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := canonical(t, t.TempDir())
	if _, err := ag.AddWorkspace(ctx, repo.DefaultUser, other, "other"); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		path string
	}{
		{"a folder with files in it", full},
		{"another workspace's folder", other},
		{"a folder inside the workspace", child},
		{"a folder that is not there", filepath.Join(root, "missing")},
	} {
		if _, err := ag.MoveWorkspace(ctx, repo.DefaultUser, w.ID, c.path); !errors.Is(err, orchestrator.ErrInvalidPath) {
			t.Errorf("%s: %v, want an invalid path", c.what, err)
		}
	}
	if saved, err := db.WorkspaceOf(repo.DefaultUser, w.ID); err != nil || saved.RootPath != root {
		t.Errorf("the workspace moved to %q (%v), want it at %s", saved.RootPath, err, root)
	}
	if _, err := os.Stat(filepath.Join(full, "theirs.txt")); err != nil {
		t.Errorf("another workspace's files were touched: %v", err)
	}
}

// canonical is the path the sandbox would store: symlinks resolved, as /var is /private/var on macOS.
func canonical(t *testing.T, path string) string {
	t.Helper()
	abs, err := sandbox.CanonicalDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

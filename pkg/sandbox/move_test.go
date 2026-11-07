package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A folder moves onto an empty folder a user picked, and never over one that holds something.
func TestMoveReplacesAnEmptyFolderAndRefusesAFullOne(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(from, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "notes.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(root, "moved")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	box := Here()
	if err := box.Move(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the old folder is still there: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(to, "notes.txt")); err != nil || string(body) != "kept\n" {
		t.Errorf("notes.txt holds %q (%v)", body, err)
	}
	if st, err := os.Stat(filepath.Join(to, "src")); err != nil || !st.IsDir() {
		t.Errorf("the folders under it did not come along: %v", err)
	}

	full := filepath.Join(root, "full")
	if err := os.Mkdir(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "theirs.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := box.Move(context.Background(), to, full); err == nil {
		t.Error("a folder with files in it was replaced")
	}
	if _, err := os.Stat(filepath.Join(full, "theirs.txt")); err != nil {
		t.Errorf("the files in the way were touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "notes.txt")); err != nil {
		t.Errorf("the refused move took the folder away: %v", err)
	}

	missing := filepath.Join(root, "missing")
	if err := box.Move(context.Background(), missing, filepath.Join(root, "nowhere")); err == nil {
		t.Error("a folder that is not there was moved")
	}

	// A move that fails for another reason leaves the empty folder the user picked as it was.
	spare := filepath.Join(root, "spare")
	if err := os.Mkdir(spare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := box.Move(context.Background(), missing, spare); err == nil {
		t.Error("a folder that is not there was moved onto the folder the user picked")
	}
	if st, err := os.Stat(spare); err != nil || !st.IsDir() {
		t.Errorf("the empty folder the user picked was taken away: %v", err)
	}
}

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestAWorkspacesFilesAreReadInsideItsRootOnly(t *testing.T) {
	f := setup(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(dir, "src", ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "file10.txt"), []byte("ten"), 0o600)
	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("two"), 0o600)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{1, 0, 2}, 0o600)
	os.WriteFile(filepath.Join(dir, "dot.png"), []byte("\x89PNG"), 0o600)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), nil, 0o600)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "away"))
	w := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", "", dir, "files"))

	entries := must[[]FileEntry](t)(f.a.ListFiles(f.ctx, "u", w.ID, ""))
	var names []string
	for _, e := range entries {
		names = append(names, string(e.Kind)+":"+e.Name)
	}
	want := []string{"folder:away", "folder:src", "file:blob.bin", "file:dot.png", "file:file2.txt", "file:file10.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("the root lists %q", names)
	}
	if _, err := f.a.ListFiles(f.ctx, "u", w.ID, "away"); err == nil {
		t.Error("a folder outside the workspace was listed through a symlink")
	}
	if _, err := f.a.PreviewFile(f.ctx, "u", w.ID, "../"+filepath.Base(outside)+"/secret"); err == nil {
		t.Error("a file outside the workspace was read")
	}
	if p := must[Preview](t)(f.a.PreviewFile(f.ctx, "u", w.ID, "file2.txt")); p.Kind != PreviewText || p.Text != "two" || p.Size != 3 {
		t.Errorf("the text file %+v", p)
	}
	if p := must[Preview](t)(f.a.PreviewFile(f.ctx, "u", w.ID, "blob.bin")); p.Kind != PreviewBinary {
		t.Errorf("the binary file %+v", p)
	}
	if p := must[Preview](t)(f.a.PreviewFile(f.ctx, "u", w.ID, "dot.png")); p.Kind != PreviewImage || p.MimeType != "image/png" {
		t.Errorf("the image %+v", p)
	}
	if data, mime, err := f.a.OpenFile(f.ctx, "u", w.ID, "dot.png"); err != nil || string(data) != "\x89PNG" || mime != "image/png" {
		t.Errorf("the image read whole: %q %s %v", data, mime, err)
	}
	for rel, want := range map[string]FileKind{"src": FileKindFolder, "file2.txt": FileKindFile, "missing": "", "away/secret": ""} {
		if got := must[FileKind](t)(f.a.StatFile(f.ctx, "u", w.ID, rel)); got != want {
			t.Errorf("%s is %q, not %q", rel, got, want)
		}
	}
	if _, err := f.a.ListFiles(f.ctx, "v", w.ID, ""); err == nil {
		t.Error("another user read the workspace")
	}

	var scratch account.Workspace
	f.st.Read(f.ctx, func(r store.Reader) (err error) {
		envs, err := r.Environments("u")
		if err == nil {
			scratch, err = r.Scratch("u", envs[0].ID)
		}
		return err
	})
	p := must[place](t)(f.a.place(f.ctx, "u", scratch.ID))
	os.WriteFile(filepath.Join(p.root, "note.md"), []byte("scratch"), 0o600)
	if got := must[Preview](t)(f.a.PreviewFile(f.ctx, "u", scratch.ID, "note.md")); got.Text != "scratch" {
		t.Errorf("the scratch workspace's file %+v", got)
	}
}

func TestAPersonCommitsAWorkspacesChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	f := setup(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(dir, "project")
	os.Mkdir(project, 0o755)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	w := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", "", project, "project"))
	if c := must[Changes](t)(f.a.Changes(f.ctx, "u", w.ID)); c.Repo {
		t.Errorf("changes outside a repository %+v", c)
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(project, "kept.txt"), []byte("one\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("x\n"), 0o600)
	git("add", ".")
	git("commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(project, "kept.txt"), []byte("one\ntwo\n"), 0o600)
	os.WriteFile(filepath.Join(project, "new.txt"), []byte("new\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("changed\n"), 0o600)

	c := must[Changes](t)(f.a.Changes(f.ctx, "u", w.ID))
	if !c.Repo || c.Head.Branch != "main" || len(c.Files) != 2 {
		t.Fatalf("the changes %+v", c)
	}
	kept := c.Files[slices.IndexFunc(c.Files, func(x FileChange) bool { return x.Path == "kept.txt" })]
	if kept.Kind != ChangeModified || kept.Staged != StagedNone || kept.Added == nil || *kept.Added != 1 {
		t.Errorf("the modified file %+v", kept)
	}
	if d := must[FileDiff](t)(f.a.Diff(f.ctx, "u", w.ID, "kept.txt", false)); !strings.Contains(d.Text, "+two") {
		t.Errorf("the diff %q", d.Text)
	}
	if d := must[FileDiff](t)(f.a.Diff(f.ctx, "u", w.ID, "new.txt", true)); !strings.Contains(d.Text, "+new") {
		t.Errorf("the untracked file's diff %q", d.Text)
	}
	if err := f.a.Stage(f.ctx, "u", w.ID, []string{"../outside.txt"}); err == nil {
		t.Error("a file outside the workspace was staged")
	}
	if err := f.a.Stage(f.ctx, "u", w.ID, []string{"kept.txt", "new.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Unstage(f.ctx, "u", w.ID, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.CommitChanges(f.ctx, "u", w.ID, "  "); err == nil {
		t.Error("a commit without a message")
	}
	t.Setenv("GIT_AUTHOR_NAME", "T")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "T")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	commit := must[Commit](t)(f.a.CommitChanges(f.ctx, "u", w.ID, "second"))
	if commit.Subject != "second" || commit.Hash == "" {
		t.Errorf("the commit %+v", commit)
	}
	c = must[Changes](t)(f.a.Changes(f.ctx, "u", w.ID))
	if len(c.Files) != 1 || c.Files[0].Path != "new.txt" || c.Files[0].Kind != ChangeUntracked {
		t.Errorf("left after the commit %+v", c.Files)
	}
	os.WriteFile(filepath.Join(project, "kept.txt"), []byte("three\n"), 0o600)
	if err := f.a.Discard(f.ctx, "u", w.ID, "kept.txt", ChangeModified); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "kept.txt")); string(data) != "one\ntwo\n" {
		t.Errorf("discarded, the file holds %q", data)
	}
	if runtime.GOOS != "darwin" {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		if err := f.a.Discard(f.ctx, "u", w.ID, "new.txt", ChangeUntracked); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(project, "new.txt")); !os.IsNotExist(err) {
			t.Errorf("the untracked file is still there: %v", err)
		}
	}
}

func TestAPersonBrowsesAnEnvironmentsFoldersAndMakesOne(t *testing.T) {
	f := setup(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.Mkdir(filepath.Join(dir, "b"), 0o755)
	os.Mkdir(filepath.Join(dir, "a"), 0o755)
	os.Mkdir(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, "file"), nil, 0o600)
	got := must[Folder](t)(f.a.ListFolders(f.ctx, "u", "", dir))
	if got.Path != dir || got.Parent != filepath.Dir(dir) || !slices.Equal(got.Folders, []string{"a", "b"}) {
		t.Errorf("the folder %+v", got)
	}
	if own := must[Folder](t)(f.a.ListFolders(f.ctx, "u", "", "")); own.Path == "" {
		t.Errorf("the environment's own folder %+v", own)
	}
	if _, err := f.a.ListFolders(f.ctx, "u", "", "relative"); err == nil {
		t.Error("a relative path was listed")
	}
	made := must[string](t)(f.a.MakeFolder(f.ctx, "u", "", dir, "c"))
	if st, err := os.Stat(made); err != nil || !st.IsDir() || made != filepath.Join(dir, "c") {
		t.Errorf("made %s: %v", made, err)
	}
	for _, name := range []string{"", "..", "x/y"} {
		if _, err := f.a.MakeFolder(f.ctx, "u", "", dir, name); err == nil {
			t.Errorf("a folder named %q was made", name)
		}
	}
}

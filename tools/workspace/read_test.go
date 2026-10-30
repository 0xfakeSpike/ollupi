package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/tools"
)

// readFixture is a project with a .gitignore, a closed folder beside it, and a link from the project into it.
func readFixture(t *testing.T) (root string, call func(tool string, args map[string]any) (any, error), content func(tool string, args map[string]any) (tools.Content, error)) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, secrets := filepath.Join(base, "p"), filepath.Join(base, "secrets")
	files := map[string]string{
		"p/.git/HEAD":           "ref: main\n",
		"p/.gitignore":          "build/\n*.log\n!keep.log\n",
		"p/src/a.go":            "package a\n\nfunc A() {}\n",
		"p/src/b.go":            "package a\n\n// TODO one\nfunc B() {}\n// todo two\n",
		"p/src/view.tsx":        "export const V = 1\n",
		"p/src/.gitignore":      "gen.go\n",
		"p/src/gen.go":          "package a // TODO generated\n",
		"p/build/out.go":        "package out // TODO built\n",
		"p/debug.log":           "TODO log\n",
		"p/keep.log":            "TODO kept\n",
		"p/lines.txt":           "one\ntwo\nthree\nfour\nfive\n",
		"secrets/key.txt":       "TODO secret\n",
		"p/docs/notes.md":       "# Notes\n",
		"p/docs/deep/readme.md": "deep\n",
	}
	for name, body := range files {
		path := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "image.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o644)
	if err := os.Symlink(filepath.Join(secrets, "key.txt"), filepath.Join(root, "src", "key.txt")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "src", "a.go"), old, old)

	r, err := New(Options{Config: Config{WorkspaceDirs: []string{root}, SandboxMode: "full-access"}, DenyRead: []string{secrets}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	specs, _ := r.ToolList(context.Background())
	spec := map[string]*tools.Spec{}
	for _, s := range specs {
		spec[s.Name] = s
	}
	ctx := tools.WithCaller(context.Background(), tools.Caller{Root: root})
	call = func(tool string, args map[string]any) (any, error) {
		if err := tools.ValidateArguments(args, spec[tool].InputSchema); err != nil {
			return nil, err
		}
		res, err := r.Invoke(ctx, tool, args)
		return res.Output, err
	}
	content = func(tool string, args map[string]any) (tools.Content, error) {
		return spec[tool].PermissionContent(ctx, args)
	}
	for _, name := range []string{ToolReadFile, ToolGlob, ToolGrep} {
		if a := spec[name].Annotations; a == nil || !a.ReadOnlyHint || tools.OpenWorld(*a) || spec[name].Local != nil {
			t.Fatalf("%s is not a plain read-only tool: %+v", name, a)
		}
	}
	return root, call, content
}

func TestReadFile(t *testing.T) {
	root, call, content := readFixture(t)
	out, err := call(ToolReadFile, map[string]any{"path": "lines.txt", "offset": 2, "limit": 2})
	if got := out.(ReadFileOutput); err != nil || got.Text != "2\ttwo\n3\tthree\n" || got.StartLine != 2 || got.EndLine != 3 || got.TotalLines != 5 {
		t.Fatalf("window: %v %+v", err, out)
	}
	out, err = call(ToolReadFile, map[string]any{"path": filepath.Join(root, "src/a.go")})
	if got := out.(ReadFileOutput); err != nil || got.Path != "src/a.go" || !strings.HasPrefix(got.Text, "1\tpackage a\n") || got.TotalLines != 3 {
		t.Fatalf("absolute path: %v %+v", err, out)
	}
	out, err = call(ToolReadFile, map[string]any{"path": "image.png"})
	if got := out.(ReadFileOutput); err != nil || !got.Binary || got.MimeType != "image/png" || got.Text != "" {
		t.Fatalf("binary: %v %+v", err, out)
	}
	for _, bad := range []map[string]any{
		{"path": "src/key.txt"},              // a link into a closed folder
		{"path": "../secrets/key.txt"},       // the closed folder itself
		{"path": "lines.txt", "offset": 9},   // past the end
		{"path": "src"},                      // a folder
		{"path": "lines.txt", "limit": 5000}, // over the limit
		{"path": "missing.txt"},
	} {
		if out, err := call(ToolReadFile, bad); err == nil {
			t.Errorf("%v read: %+v", bad, out)
		}
	}
	if c, err := content(ToolReadFile, map[string]any{"path": "SRC/../src/A.go"}); err != nil || c.Match != "src/a.go" {
		t.Errorf("permission content: %+v %v", c, err)
	}
	if _, err := content(ToolReadFile, map[string]any{"path": "src/key.txt"}); err == nil {
		t.Error("permission content of a closed file")
	}
}

func TestGlob(t *testing.T) {
	_, call, content := readFixture(t)
	out, err := call(ToolGlob, map[string]any{"pattern": "**/*.go"})
	if got := out.(GlobOutput); err != nil || strings.Join(got.Files, " ") != "src/b.go src/a.go" || got.Skipped != 1 {
		t.Fatalf("**/*.go, newest first, ignored files left out, the closed link counted: %v %+v", err, out)
	}
	out, _ = call(ToolGlob, map[string]any{"pattern": "*.{log,txt}"})
	if got := out.(GlobOutput); strings.Join(got.Files, " ") != "keep.log lines.txt" && strings.Join(got.Files, " ") != "lines.txt keep.log" {
		t.Errorf("braces and a re-included file: %+v", got)
	}
	out, _ = call(ToolGlob, map[string]any{"pattern": "**/*.md", "path": "docs"})
	if got := out.(GlobOutput); len(got.Files) != 2 || got.Path != "docs" {
		t.Errorf("a folder: %+v", got)
	}
	out, _ = call(ToolGlob, map[string]any{"pattern": "*", "path": "src"})
	if got := out.(GlobOutput); strings.Contains(strings.Join(got.Files, " "), "key.txt") || got.Skipped != 1 {
		t.Errorf("a link into a closed folder is skipped and counted: %+v", got)
	}
	if c, err := content(ToolGlob, map[string]any{"pattern": "*.go", "path": "src"}); err != nil || c.Match != "src" || c.Text != "*.go in src" {
		t.Errorf("permission content: %+v %v", c, err)
	}
}

func TestGrep(t *testing.T) {
	root, call, _ := readFixture(t)
	out, err := call(ToolGrep, map[string]any{"pattern": "todo", "ignore_case": true, "files_only": true})
	got := out.(GrepOutput)
	var names []string
	for _, f := range got.Files {
		names = append(names, f.Path)
	}
	if err != nil || strings.Join(names, " ") != "keep.log src/b.go" || got.Matches != 3 {
		t.Fatalf("files only, in path order, ignored and closed files left out: %v %+v", err, got)
	}
	out, _ = call(ToolGrep, map[string]any{"pattern": "TODO", "path": "src", "include": "*.go", "context": 1})
	got = out.(GrepOutput)
	if len(got.Files) != 1 || got.Matches != 1 {
		t.Fatalf("include: %+v", got)
	}
	want := []GrepLine{{Line: 2, Text: ""}, {Line: 3, Text: "// TODO one", Match: true}, {Line: 4, Text: "func B() {}"}}
	if lines := got.Files[0].Lines; len(lines) != len(want) || lines[0] != want[0] || lines[1] != want[1] || lines[2] != want[2] {
		t.Errorf("context lines: %+v", lines)
	}
	out, err = call(ToolGrep, map[string]any{"pattern": "two", "path": filepath.Join(root, "lines.txt")})
	if got := out.(GrepOutput); err != nil || len(got.Files) != 1 || got.Files[0].Lines[0].Line != 2 {
		t.Errorf("one file: %v %+v", err, out)
	}
	if _, err := call(ToolGrep, map[string]any{"pattern": "("}); err == nil {
		t.Error("an invalid expression searched")
	}
}

func TestIgnoreRules(t *testing.T) {
	f := parseIgnore("", "# comment\n/top.txt\nnode_modules/\n*.tmp\n!keep.tmp\ndocs/**/draft.md\n")
	g := &ignores{top: "/r", files: []ignoreFile{f, parseIgnore("sub", "local.txt\n")}}
	for path, want := range map[string]bool{
		"/r/top.txt": true, "/r/a/top.txt": false, "/r/node_modules": true, "/r/a/x.tmp": true, "/r/keep.tmp": false,
		"/r/docs/a/b/draft.md": true, "/r/docs/draft.md": true, "/r/sub/local.txt": true, "/r/local.txt": false,
	} {
		if got := g.ignored(path, path == "/r/node_modules"); got != want {
			t.Errorf("%s ignored = %v", path, got)
		}
	}
	if g.ignored("/r/node_modules", false) {
		t.Error("a folder-only rule matched a file")
	}
}

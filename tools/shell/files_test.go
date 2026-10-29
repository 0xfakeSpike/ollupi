package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
)

func TestUnifiedDiff(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	after := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk"
	diff, added, removed := unifiedDiff("x.txt", before, after, false)
	want := `--- a/x.txt
+++ b/x.txt
@@ -1,5 +1,5 @@
 a
-b
+B
 c
 d
 e
@@ -8,3 +8,4 @@
 h
 i
 j
+k
\ No newline at end of file
`
	if diff != want || added != 2 || removed != 1 {
		t.Errorf("diff (+%d -%d):\n%s", added, removed, diff)
	}
	diff, added, _ = unifiedDiff("new.txt", "", "one\n", true)
	if diff != "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,1 @@\n+one\n" || added != 1 {
		t.Errorf("new file diff:\n%s", diff)
	}
}

func TestFileTools(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(Options{Config: Config{WorkspaceDirs: []string{root}, SandboxMode: "full-access"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	specs, _ := r.ToolList(context.Background())
	spec := map[string]*tools.Spec{}
	for _, s := range specs {
		spec[s.Name] = s
	}
	ctx := tools.WithCaller(context.Background(), tools.Caller{Root: root})
	run := func(tool string, args map[string]any) (tools.Result, *tools.Prepared, error) {
		p, err := spec[tool].Local.Prepare(ctx, args)
		if err != nil {
			return tools.Result{}, nil, err
		}
		res, err := spec[tool].Local.Run(ctx, args, p)
		return res, p, err
	}

	if _, p, err := run(ToolWriteFile, map[string]any{"path": "src/a.go", "content": "package a\n"}); err != nil || p.Files[0].Before != "" || !strings.HasPrefix(p.Files[0].Diff, "--- /dev/null") {
		t.Fatalf("write: %v %+v", err, p)
	}
	args := map[string]any{"path": "src/a.go", "edits": []any{map[string]any{"old_text": "package a", "new_text": "package b"}}}
	content, err := spec[ToolEditFile].PermissionContent(ctx, args)
	if err != nil || content.Match != "src/a.go" || !strings.Contains(content.Text, "+package b") {
		t.Fatalf("content %+v: %v", content, err)
	}
	p, err := spec[ToolEditFile].Local.Prepare(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "src/a.go"), []byte("package a\n// changed\n"), 0o644)
	if _, err := spec[ToolEditFile].Local.Run(ctx, args, p); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Errorf("edit of a changed file: %v", err)
	}
	if res, _, err := run(ToolEditFile, args); err != nil || res.Output.(FileOutput).Added != 1 {
		t.Fatalf("edit: %v %+v", err, res)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "src/a.go")); string(body) != "package b\n// changed\n" {
		t.Errorf("file holds %q", body)
	}
	for _, bad := range []map[string]any{
		{"path": "../outside.txt", "content": "x"},
		{"path": "src/a.go", "edits": []any{map[string]any{"old_text": "missing", "new_text": "x"}}},
		{"path": "src/none.go", "edits": []any{map[string]any{"old_text": "x", "new_text": "y"}}},
	} {
		tool := ToolEditFile
		if _, ok := bad["content"]; ok {
			tool = ToolWriteFile
		}
		if _, _, err := run(tool, bad); err == nil {
			t.Errorf("%s %v succeeded", tool, bad)
		}
	}
}

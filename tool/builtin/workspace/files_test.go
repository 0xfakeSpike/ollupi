package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestUnifiedDiff(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	after := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk"
	diff, added, removed, err := unifiedDiff("x.txt", before, after, false)
	if err != nil {
		t.Fatal(err)
	}
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
	diff, added, _, err = unifiedDiff("new.txt", "", "one\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+one\n" || added != 1 {
		t.Errorf("new file diff:\n%s", diff)
	}
}

func TestAFileRewrittenWholeDiffsQuickly(t *testing.T) {
	var before, after strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&before, "old line %d\n", i)
		fmt.Fprintf(&after, "new line %d\n", i)
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	allocated := stats.TotalAlloc
	_, added, removed, err := unifiedDiff("x.txt", before.String(), after.String(), false)
	if err != nil {
		t.Fatal(err)
	}
	if added != 20000 || removed != 20000 {
		t.Errorf("+%d -%d, want every line replaced", added, removed)
	}
	runtime.ReadMemStats(&stats)
	if used := stats.TotalAlloc - allocated; used > 256<<20 {
		t.Errorf("the diff allocated %d MB", used>>20)
	}
}

func TestFileTools(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{Config: Config{WorkspaceDirs: []string{root}, SandboxMode: "full-access"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	specs, _ := r.Tools(context.Background())
	spec := map[string]kit.Spec{}
	for _, s := range specs {
		spec[s.Name] = s
	}
	ctx, caller := context.Background(), tool.Caller{Root: root}
	run := func(name string, args map[string]any) (kit.Result, *Prepared, error) {
		p, err := r.locals()[name].prepare(ctx, tool.Call{Caller: caller, Tool: name, Arguments: args})
		if err != nil {
			return kit.Result{}, nil, err
		}
		res, err := r.locals()[name].run(ctx, tool.Call{Caller: caller, Tool: name, Arguments: args}, p)
		return res, p, err
	}

	if _, p, err := run(ToolWriteFile, map[string]any{"path": "src/a.go", "content": "package a\n"}); err != nil || p.Files[0].Before != "" || !strings.HasPrefix(p.Files[0].Diff, "--- /dev/null") {
		t.Fatalf("write: %v %+v", err, p)
	}
	args := map[string]any{"path": "src/a.go", "edits": []any{map[string]any{"old_text": "package a", "new_text": "package b"}}}
	content, err := spec[ToolEditFile].Content(ctx, tool.Call{Caller: caller, Arguments: args})
	if err != nil || content.Match != "src/a.go" || !strings.Contains(content.Text, "+package b") {
		t.Fatalf("content %+v: %v", content, err)
	}
	p, err := r.locals()[ToolEditFile].prepare(ctx, tool.Call{Caller: caller, Tool: ToolEditFile, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "src/a.go"), []byte("package a\n// changed\n"), 0o644)
	if _, err := r.locals()[ToolEditFile].run(ctx, tool.Call{Caller: caller, Tool: ToolEditFile, Arguments: args}, p); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Errorf("edit of a changed file: %v", err)
	}
	if res, _, err := run(ToolEditFile, args); err != nil || res.Output.(FileOutput).Added != 1 || !strings.Contains(res.Output.(FileOutput).Diff, "+package b") {
		t.Fatalf("edit, with its diff: %v %+v", err, res)
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

func TestAFailedEditSaysWhereTheTextDiffers(t *testing.T) {
	c := fileChange{rel: "tasks.md", exists: true, before: "# Tasks\n| 编号\u3000| 标题 |\n| a | b |\n| a | b |\n\tx := “1”\n"}
	edits := func(e ...map[string]any) tool.Call {
		list := make([]any, len(e))
		for i := range e {
			list[i] = e[i]
		}
		return tool.Call{Tool: ToolEditFile, Arguments: map[string]any{"path": "tasks.md", "edits": list}}
	}
	ok := map[string]any{"old_text": "# Tasks", "new_text": "# Todo"}
	_, err := newContent(c, edits(ok, map[string]any{"old_text": "| 编号 | 标题 |", "new_text": "x"}))
	if err == nil || !strings.Contains(err.Error(), `at line 2 except`) || !strings.Contains(err.Error(), "old_text has U+0020 \" \" where the file has U+3000 \"\\u3000\"") ||
		!strings.Contains(err.Error(), "none of the 2 edits were applied") {
		t.Errorf("a full-width space: %v", err)
	}
	_, err = newContent(c, edits(map[string]any{"old_text": "    x := \"1\"", "new_text": "x"}))
	if err == nil || !strings.Contains(err.Error(), `at line 5 except`) || !strings.Contains(err.Error(), `old_text has "    " where the file has U+0009 "\t"`) {
		t.Errorf("spaces for a tab: %v", err)
	}
	_, err = newContent(c, edits(map[string]any{"old_text": "| a | b |\n| c | d |", "new_text": "x"}))
	if err == nil || !strings.Contains(err.Error(), `first 12 characters match, up to line 4, where old_text has U+0063 "c" but the file has U+0061 "a"`) {
		t.Errorf("text that changed since it was read: %v", err)
	}
	_, err = newContent(c, edits(map[string]any{"old_text": "| a | b |", "new_text": "x"}))
	if err == nil || !strings.Contains(err.Error(), "at lines 3, 4") || strings.Contains(err.Error(), "none of") {
		t.Errorf("text that appears twice: %v", err)
	}
	_, err = newContent(c, edits(map[string]any{"old_text": "nothing like it", "new_text": "x"}))
	if err == nil || strings.Contains(err.Error(), "characters match") {
		t.Errorf("text with no close match: %v", err)
	}
}

func TestAChangeIsShownRelativeToItsWorkspaceAndWholeWithoutOne(t *testing.T) {
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{Config: Config{WorkspaceDirs: []string{scratch}, ExtraWritable: []string{project}, SandboxMode: "full-access"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	write := func(caller tool.Caller) string {
		t.Helper()
		ctx, call := context.Background(), tool.Call{Caller: caller, Tool: ToolWriteFile, Arguments: map[string]any{"path": "notes/report.md", "content": "x\n"}}
		p, err := r.locals()[ToolWriteFile].prepare(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.locals()[ToolWriteFile].run(ctx, call, p); err != nil {
			t.Fatal(err)
		}
		return p.Files[0].Path
	}
	// A workspace can move: its files are named from its root, which the
	// client knows as it is now.
	if got := write(tool.Caller{Root: project}); got != "notes/report.md" {
		t.Errorf("in a workspace the change names %q", got)
	}
	// Without one the client has no root to join a relative path to.
	if got, want := write(tool.Caller{}), filepath.Join(scratch, "notes/report.md"); got != want {
		t.Errorf("without a workspace the change names %q, want %q", got, want)
	}
}

func TestValuesOverTheirLimitRunWithTheMostAllowed(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("1\n2\nneedle\n4\n"), 0o644)
	r, err := New(Options{Config: Config{WorkspaceDirs: []string{root}, SandboxMode: "full-access"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	specs, _ := r.Tools(ctx)
	schemas := map[string]*jsonschema.Resolved{}
	for _, s := range specs {
		resolved, err := s.InputSchema.(*jsonschema.Schema).Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[s.Name] = resolved
	}
	run := map[string]any{"program": "true", "timeout_sec": 100000}
	grep := map[string]any{"pattern": "needle", "context": 50}
	for name, args := range map[string]map[string]any{ToolRunCommand: run, ToolGrep: grep} {
		if err := schemas[name].Validate(args); err != nil {
			t.Errorf("%s %v does not pass its schema: %v", name, args, err)
		}
	}
	call := tool.Call{Caller: tool.Caller{Root: root}, Tool: ToolRunCommand, Arguments: run}
	p, err := r.locals()[ToolRunCommand].prepare(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.locals()[ToolRunCommand].run(ctx, call, p)
	if out, _ := res.Output.(CommandOutput); err != nil || p.TimeoutSec != 600 || !strings.Contains(out.Notice, "ran with 600") {
		t.Errorf("an over-long timeout: %v %d %+v", err, p.TimeoutSec, res.Output)
	}
	res, err = r.Invoke(ctx, tool.Call{Caller: tool.Caller{Root: root}, Tool: ToolGrep, Arguments: grep})
	if out, _ := res.Output.(GrepOutput); err != nil || len(out.Files) != 1 || len(out.Files[0].Lines) != 4 || !strings.Contains(out.Notice, "shows 10 lines") {
		t.Errorf("a context over its limit: %v %+v", err, res.Output)
	}
}

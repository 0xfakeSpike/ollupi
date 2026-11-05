package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
)

// memoryMachine is a cloud sandbox that holds its files in memory, at paths this machine does not have, so a tool
// that went around the box would find nothing.
type memoryMachine struct {
	mu    sync.Mutex
	files fstest.MapFS
	ran   []sandbox.Command
}

func (m *memoryMachine) rel(p string) string { return strings.TrimPrefix(path.Clean(p), "/") }

func (m *memoryMachine) Run(_ context.Context, c sandbox.Command) (sandbox.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ran = append(m.ran, c)
	return sandbox.Result{Stdout: "ran in the machine", ExitCode: 3}, nil
}

func (m *memoryMachine) Start(context.Context, sandbox.Command, io.Writer) (sandbox.Process, error) {
	return nil, errors.New("no process runs here")
}

func (m *memoryMachine) Stat(_ context.Context, p string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files.Stat(m.rel(p))
}

func (m *memoryMachine) ReadDir(_ context.Context, p string) ([]fs.DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files.ReadDir(m.rel(p))
}

func (m *memoryMachine) Open(_ context.Context, p string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, err := m.files.ReadFile(m.rel(p))
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (m *memoryMachine) WriteFile(_ context.Context, p string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[m.rel(p)] = &fstest.MapFile{Data: data, Mode: perm, ModTime: time.Now()}
	return nil
}

func (m *memoryMachine) Close() error { return nil }

const machineRoot = "/cloud/work"

func machineWith(files map[string]string) *memoryMachine {
	m := &memoryMachine{files: fstest.MapFS{}}
	for name, body := range files {
		m.files[m.rel(machineRoot+"/"+name)] = &fstest.MapFile{Data: []byte(body), Mode: 0o644, ModTime: time.Now()}
	}
	return m
}

// Every workspace tool works on the user's own machine, with nothing of it on this one.
func TestToolsRunOnTheUsersMachine(t *testing.T) {
	machines := map[string]*memoryMachine{
		"a": machineWith(map[string]string{"src/a.go": "package a\n// needle\n", ".git/HEAD": "ref\n", ".gitignore": "gen/\n", "gen/x.go": "needle\n"}),
		"b": machineWith(map[string]string{"src/b.go": "package b\n"}),
	}
	roots := []string{machineRoot}
	r, err := New(Options{
		Box: sandbox.Remote(nil, roots, "A cloud sandbox of your own"),
		BoxFor: func(user string) (sandbox.Box, error) {
			m, ok := machines[user]
			if !ok {
				return nil, errors.New("no sandbox for this user")
			}
			return sandbox.Remote(m, roots, "A cloud sandbox of your own"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	specs, err := r.ToolList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]*tools.Spec{}
	for _, s := range specs {
		spec[s.Name] = s
	}
	as := func(user string) context.Context {
		return tools.WithCaller(context.Background(), tools.Caller{UserID: user})
	}
	read := func(user, tool string, args map[string]any) any {
		t.Helper()
		res, err := r.Invoke(as(user), tool, args)
		if err != nil {
			t.Fatalf("%s as %s: %v", tool, user, err)
		}
		return res.Output
	}
	act := func(user, tool string, args map[string]any) (tools.Result, error) {
		t.Helper()
		ctx := as(user)
		p, err := spec[tool].Local.Prepare(ctx, args)
		if err != nil {
			return tools.Result{}, err
		}
		return spec[tool].Local.Run(ctx, args, p)
	}

	if out := read("a", ToolReadFile, map[string]any{"path": "src/a.go"}).(ReadFileOutput); !strings.Contains(out.Text, "needle") {
		t.Errorf("read: %+v", out)
	}
	if out := read("a", ToolGlob, map[string]any{"pattern": "**/*.go"}).(GlobOutput); !slices.Equal(out.Files, []string{"src/a.go"}) {
		t.Errorf("glob, with gen/ ignored: %+v", out)
	}
	if out := read("a", ToolGrep, map[string]any{"pattern": "needle"}).(GrepOutput); len(out.Files) != 1 || out.Files[0].Path != "src/a.go" {
		t.Errorf("grep: %+v", out)
	}
	if out := read("b", ToolGlob, map[string]any{"pattern": "**/*.go"}).(GlobOutput); !slices.Equal(out.Files, []string{"src/b.go"}) {
		t.Errorf("b's files: %+v", out)
	}

	if _, err := act("a", ToolWriteFile, map[string]any{"path": "src/new.go", "content": "package new\n"}); err != nil {
		t.Fatal(err)
	}
	edit := map[string]any{"path": "src/new.go", "edits": []any{map[string]any{"old_text": "package new", "new_text": "package old"}}}
	if _, err := act("a", ToolEditFile, edit); err != nil {
		t.Fatal(err)
	}
	if got := string(machines["a"].files[strings.TrimPrefix(machineRoot, "/")+"/src/new.go"].Data); got != "package old\n" {
		t.Errorf("the file on a's machine: %q", got)
	}
	if _, ok := machines["b"].files[strings.TrimPrefix(machineRoot, "/")+"/src/new.go"]; ok {
		t.Error("a's file reached b's machine")
	}
	if _, err := act("a", ToolWriteFile, map[string]any{"path": "/etc/passwd", "content": "x"}); err == nil {
		t.Error("a write outside the roots went through")
	}

	res, err := act("a", ToolRunCommand, map[string]any{"program": "make", "args": []any{"test"}, "work_dir": "src"})
	if err != nil {
		t.Fatal(err)
	}
	if out := res.Output.(CommandOutput); out.Stdout != "ran in the machine" || out.ExitCode != 3 {
		t.Errorf("command: %+v", out)
	}
	if ran := machines["a"].ran; len(ran) != 1 || ran[0].Program != "make" || ran[0].Cwd != machineRoot+"/src" {
		t.Errorf("a's machine ran %+v", ran)
	}
	if ran := machines["b"].ran; len(ran) != 0 {
		t.Errorf("b's machine ran %+v", ran)
	}

	if _, err := act("a", ToolRunCommand, map[string]any{"program": "make", "sandbox_permissions": sandboxRequireEscalated, "justification": "x"}); err == nil {
		t.Error("a command left the machine")
	}
	if _, ok := spec[ToolRunCommand].InputSchema.(map[string]any)["properties"].(map[string]any)["sandbox_permissions"]; ok {
		t.Error("the tool offers to leave a sandbox that has no way out")
	}
	if strings.Contains(spec[ToolRunCommand].Description, "require_escalated") {
		t.Error("the tool describes leaving the sandbox")
	}
	if !strings.Contains(spec[ToolRunCommand].Description, "A cloud sandbox of your own") {
		t.Errorf("the tool does not describe its sandbox: %s", spec[ToolRunCommand].Description)
	}

	if _, err := r.Invoke(as("nobody"), ToolGlob, map[string]any{"pattern": "*"}); err == nil {
		t.Error("a user with no sandbox reached one")
	}
}

func TestACommandPreparedOnAMachineIsConfined(t *testing.T) {
	m := machineWith(map[string]string{"a.txt": "a\n"})
	r, err := New(Options{Box: sandbox.Remote(m, []string{machineRoot}, "cloud")})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p, err := r.prepareCommand(context.Background(), map[string]any{"program": "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Sandboxed || p.Program != "ls" || p.Cwd != machineRoot {
		t.Errorf("prepared %s", fmt.Sprintf("%+v", p))
	}
}

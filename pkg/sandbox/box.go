package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Box is where a user's commands run and files live: this machine, or a cloud sandbox of their own. The tools reach
// them only through it. Paths are the box's, and need not exist on this machine.
type Box interface {
	Run(ctx context.Context, c Command) (Result, error)
	// Stat follows links and, like the rest, reports a missing path with an error that is fs.ErrNotExist.
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// WriteFile makes the folders above path and replaces the file whole, so a reader never sees half of it.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// Canonical resolves path, which need not exist yet, to the one name the rules below are checked against.
	Canonical(ctx context.Context, path string) (string, error)
	// Directory is Canonical for a folder that must exist.
	Directory(ctx context.Context, path string) (string, error)
	// Executable finds the program a command would run from cwd.
	Executable(ctx context.Context, program, cwd string) (string, error)

	// Enforced says commands are confined, so they need not ask the user first.
	Enforced() bool
	// CanEscalate says a command can leave the confinement, once the user agrees.
	CanEscalate() bool
	Summary() string
	Roots() []string
	// Fingerprint changes when the rules do, so a call prepared under other rules is not run.
	Fingerprint() any
	Check(dir string) error
	CheckRead(path string) error
	CheckWrite(roots []string, path string) error
	Close() error
}

// ReadFile reads a whole file from the box.
func ReadFile(ctx context.Context, b Box, path string) ([]byte, error) {
	f, err := b.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (s *Sandbox) Stat(_ context.Context, path string) (fs.FileInfo, error) { return os.Stat(path) }

func (s *Sandbox) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

func (s *Sandbox) Open(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }

func (s *Sandbox) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := errors.Join(tmp.Chmod(perm), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Sandbox) Canonical(_ context.Context, path string) (string, error) {
	return CanonicalPath(path, "")
}

func (s *Sandbox) Directory(_ context.Context, path string) (string, error) {
	return CanonicalDirectory(path)
}

func (s *Sandbox) Executable(_ context.Context, program, cwd string) (string, error) {
	var path string
	var err error
	if strings.ContainsRune(program, os.PathSeparator) {
		path = program
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
	} else {
		path, err = exec.LookPath(program)
		if err != nil {
			return "", fmt.Errorf("workspace: executable %q is not on PATH", program)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: executable %q is unavailable: %w", program, err)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if st.IsDir() || st.Mode()&0111 == 0 {
		return "", fmt.Errorf("workspace: %q is not executable", program)
	}
	return resolved, nil
}

// CanEscalate is true where there is a sandbox to leave: the command then runs on this machine.
func (s *Sandbox) CanEscalate() bool { return s.Enforced() }

// Machine is a cloud sandbox, connected and ready, in the terms its provider's SDK gives: a command with its output
// and exit code, and files by path. Creating it, finding it again by id, waking it and letting it sleep stay with the
// code that returns it, as do its network rules, which providers set on the sandbox itself.
type Machine interface {
	// Run returns when the command ends, and stops it at c.Timeout or when ctx ends. A command that ran and failed
	// is a Result with its exit code, not an error.
	Run(ctx context.Context, c Command) (Result, error)
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// Close drops the connection; the sandbox and its files stay for the next one.
	Close() error
}

// remote is a Box on a Machine. The machine is the confinement, and it holds one user's work alone, so nothing in it
// is closed to them and there is nowhere to escalate to.
type remote struct {
	Machine
	roots   []string
	summary string
}

// Remote makes a Box of a machine: its commands run in the first of roots, and files are written under them. summary
// tells the model what the machine allows. A nil machine only describes the tools.
func Remote(m Machine, roots []string, summary string) Box {
	return &remote{Machine: m, roots: roots, summary: summary}
}

func (r *remote) Run(ctx context.Context, c Command) (Result, error) {
	if c.Unsandboxed {
		return Result{}, errors.New("sandbox: a command cannot leave this sandbox")
	}
	return r.Machine.Run(ctx, c)
}

func (r *remote) Close() error {
	if r.Machine == nil {
		return nil
	}
	return r.Machine.Close()
}

func (r *remote) Canonical(_ context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("sandbox: %q is not an absolute path", path)
	}
	return filepath.Clean(path), nil
}

func (r *remote) Directory(ctx context.Context, path string) (string, error) {
	dir, err := r.Canonical(ctx, path)
	if err != nil {
		return "", err
	}
	st, err := r.Stat(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("directory %q is unavailable: %w", dir, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}
	return dir, nil
}

// Executable leaves a bare name to the machine's own PATH.
func (r *remote) Executable(ctx context.Context, program, cwd string) (string, error) {
	if !strings.ContainsRune(program, filepath.Separator) {
		return program, nil
	}
	path := program
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	st, err := r.Stat(ctx, path)
	if err != nil {
		return "", fmt.Errorf("workspace: executable %q is unavailable: %w", program, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("workspace: %q is not executable", program)
	}
	return filepath.Clean(path), nil
}

func (r *remote) Enforced() bool    { return true }
func (r *remote) CanEscalate() bool { return false }
func (r *remote) Summary() string   { return r.summary }
func (r *remote) Roots() []string   { return append([]string(nil), r.roots...) }
func (r *remote) Fingerprint() any {
	return struct {
		Roots   []string
		Summary string
	}{r.roots, r.summary}
}
func (r *remote) Check(string) error     { return nil }
func (r *remote) CheckRead(string) error { return nil }

func (r *remote) CheckWrite(roots []string, path string) error {
	for _, root := range roots {
		if Within(root, path) {
			return nil
		}
	}
	return fmt.Errorf("sandbox: %q is outside the command directories", path)
}

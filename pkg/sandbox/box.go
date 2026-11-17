package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The tools reach a user's commands and files only through a Box. Paths are the box's, and need not exist on this
// machine.
type Box interface {
	Run(ctx context.Context, c Command) (Result, error)
	// Start leaves a command running until ctx ends or the Process closes. Its Timeout does not apply.
	Start(ctx context.Context, c Command, stderr io.Writer) (Process, error)
	// Stat follows links and, like the rest, reports a missing path with an error that is fs.ErrNotExist.
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// WriteFile makes the folders above path and replaces the file whole, so a reader never sees half of it.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// Move renames a folder inside the box; to must not exist or be empty, and afterwards holds what from held. It
	// fails rather than merge, and across filesystems it fails with the system's error.
	Move(ctx context.Context, from, to string) error
	// Canonical resolves path, which need not exist yet, to the one name the rules below are checked against.
	Canonical(ctx context.Context, path string) (string, error)
	// Directory is Canonical for a folder that must exist.
	Directory(ctx context.Context, path string) (string, error)
	Executable(ctx context.Context, program, cwd string) (string, error)
	Same(ctx context.Context, a, b string) bool

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
	Transport() http.RoundTripper
	Close() error
}

// Close may be called more than once. Read ends when the command does.
type Process interface {
	io.ReadWriteCloser
}

func ReadFile(ctx context.Context, b Box, path string, limit int64) ([]byte, error) {
	f, err := b.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("sandbox: %s has more than %d bytes", path, limit)
	}
	return body, nil
}

func (s *Sandbox) Transport() http.RoundTripper { return http.DefaultTransport }

func (s *Sandbox) Stat(_ context.Context, path string) (fs.FileInfo, error) { return os.Stat(path) }

func (s *Sandbox) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

// The read rules are checked on the path Open opened, so a link put on the way after the caller's check cannot lead
// around them.
func (s *Sandbox) Open(_ context.Context, path string) (io.ReadCloser, error) { return s.open(path) }

// WriteFile writes the canonical path the caller checked: a link put in place of a folder on it since is refused.
func (s *Sandbox) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return writeFile(path, data, perm)
}

func (s *Sandbox) Move(_ context.Context, from, to string) error {
	if err := os.Rename(from, to); err != nil {
		// Go refuses to rename onto a folder that is already there, even an empty one, and the user picks the folder the
		// workspace moves into: an empty one is replaced. Any other failure leaves the folder the user picked as it was.
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
		}
		entries, readErr := os.ReadDir(to)
		if readErr != nil {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("sandbox: move %s to %s: %s is not empty", from, to, to)
		}
		if rmErr := os.Remove(to); rmErr != nil {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, rmErr)
		}
		if retry := os.Rename(from, to); retry != nil {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, retry)
		}
	}
	return nil
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

func (s *Sandbox) Same(_ context.Context, a, b string) bool {
	if a == b {
		return true
	}
	canonical, err := CanonicalDirectory(a)
	if err != nil || canonical != a {
		return false
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && os.SameFile(left, right)
}

// Here is a Box with no rules of its own, what a user has when nothing else was given them. It runs no commands.
func Here() Box { return &Sandbox{} }

// CanEscalate is true where there is a sandbox to leave: the command then runs on this machine.
func (s *Sandbox) CanEscalate() bool { return s.Enforced() }

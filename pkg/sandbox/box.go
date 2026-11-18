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

type Box interface {
	Run(ctx context.Context, c Command) (Result, error)
	Start(ctx context.Context, c Command, stderr io.Writer) (Process, error)
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	Move(ctx context.Context, from, to string) error
	Canonical(ctx context.Context, path string) (string, error)
	Directory(ctx context.Context, path string) (string, error)
	Executable(ctx context.Context, program, cwd string) (string, error)
	Same(ctx context.Context, a, b string) bool

	Enforced() bool
	CanEscalate() bool
	Summary() string
	Roots() []string
	Fingerprint() any
	Check(dir string) error
	CheckRead(path string) error
	CheckWrite(roots []string, path string) error
	Transport() http.RoundTripper
	Close() error
}

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

func (s *Sandbox) Open(_ context.Context, path string) (io.ReadCloser, error) { return s.open(path) }

func (s *Sandbox) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return writeFile(path, data, perm)
}

func (s *Sandbox) Move(_ context.Context, from, to string) error {
	if err := os.Rename(from, to); err != nil {
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
	return canonicalPath(path, "")
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

func Here() Box { return &Sandbox{} }

func (s *Sandbox) CanEscalate() bool { return s.Enforced() }

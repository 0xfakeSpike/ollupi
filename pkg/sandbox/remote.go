package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type Machine interface {
	Run(ctx context.Context, c Command) (Result, error)
	Start(ctx context.Context, c Command, stderr io.Writer) (Process, error)
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	Close() error
}

type remote struct {
	Machine
	roots     []string
	summary   string
	transport *http.Transport
}

func Remote(m Machine, roots []string, summary string) Box {
	return &remote{Machine: m, roots: roots, summary: summary, transport: PublicTransport()}
}

func (r *remote) Transport() http.RoundTripper { return r.transport }

func (r *remote) Run(ctx context.Context, c Command) (Result, error) {
	if c.FullAccess {
		return Result{}, errors.New("sandbox: this sandbox cannot give a command full access")
	}
	return r.Machine.Run(ctx, c)
}

func (r *remote) Start(ctx context.Context, c Command, stderr io.Writer) (Process, error) {
	if c.FullAccess {
		return nil, errors.New("sandbox: this sandbox cannot give a command full access")
	}
	return r.Machine.Start(ctx, c, stderr)
}

func (r *remote) Same(_ context.Context, a, b string) bool { return a == b }

func (r *remote) Move(ctx context.Context, from, to string) error {
	if r.Machine == nil {
		return errors.New("sandbox: this box has no machine to move files in")
	}
	st, err := r.Machine.Stat(ctx, to)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
	case st.IsDir():
		entries, err := r.Machine.ReadDir(ctx, to)
		if err != nil {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("sandbox: move %s to %s: %s is not empty", from, to, to)
		}
		if err := r.run(ctx, "rmdir", to); err != nil {
			return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
		}
	}
	if err := r.run(ctx, "mv", "-f", from, to); err != nil {
		return fmt.Errorf("sandbox: move %s to %s: %w", from, to, err)
	}
	return nil
}

func (r *remote) run(ctx context.Context, program string, args ...string) error {
	res, err := r.Machine.Run(ctx, Command{Program: program, Args: args, Timeout: moveTimeout})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: %s", program, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (r *remote) Close() error {
	r.transport.CloseIdleConnections()
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

func (r *remote) Enforced() bool { return true }

func (r *remote) CanEscalate() bool { return false }

func (r *remote) Summary() string { return r.summary }

func (r *remote) Roots() []string { return append([]string(nil), r.roots...) }

func (r *remote) Fingerprint() any {
	return struct {
		Roots   []string
		Summary string
	}{r.roots, r.summary}
}

func (r *remote) Check(string) error { return nil }

func (r *remote) CheckRead(string) error { return nil }

func (r *remote) CheckWrite(roots []string, path string) error {
	for _, root := range roots {
		if Within(root, path) {
			return nil
		}
	}
	return fmt.Errorf("sandbox: %q is outside the command directories", path)
}

const moveTimeout = 10 * time.Minute

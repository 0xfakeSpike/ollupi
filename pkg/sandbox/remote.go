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

// Creating, finding, waking and putting a Machine to sleep stay with the code that returns it, as do its network
// rules, which providers set on the sandbox itself.
type Machine interface {
	// A command that ran and failed is a Result with its exit code, not an error.
	Run(ctx context.Context, c Command) (Result, error)
	Start(ctx context.Context, c Command, stderr io.Writer) (Process, error)
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
	roots     []string
	summary   string
	transport *http.Transport
}

// A nil machine only describes the tools.
func Remote(m Machine, roots []string, summary string) Box {
	return &remote{Machine: m, roots: roots, summary: summary, transport: PublicTransport()}
}

// Transport keeps to public addresses: the requests leave from this process, whose network is not the user's.
func (r *remote) Transport() http.RoundTripper { return r.transport }

func (r *remote) Run(ctx context.Context, c Command) (Result, error) {
	if c.Unsandboxed {
		return Result{}, errors.New("sandbox: a command cannot leave this sandbox")
	}
	return r.Machine.Run(ctx, c)
}

func (r *remote) Start(ctx context.Context, c Command, stderr io.Writer) (Process, error) {
	if c.Unsandboxed {
		return nil, errors.New("sandbox: a command cannot leave this sandbox")
	}
	return r.Machine.Start(ctx, c, stderr)
}

func (r *remote) Same(_ context.Context, a, b string) bool { return a == b }

// The machine's files are reached only through its commands. mv puts a folder inside one that is already there, so
// an empty target is removed first.
func (r *remote) Move(ctx context.Context, from, to string) error {
	if r.Machine == nil {
		return errors.New("sandbox: this box has no machine to move files in")
	}
	st, err := r.Machine.Stat(ctx, to)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		// mv puts a folder inside one that is there, so a target it cannot see is not known to be absent.
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

// moveTimeout is how long a machine's mv may take: a move of the user's whole folder, not a command of theirs.
const moveTimeout = 10 * time.Minute

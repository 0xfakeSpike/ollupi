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
	"sync"
	"time"
)

// Box is where a user's commands run and files live: this machine, or a cloud sandbox of their own. The tools reach
// them only through it. Paths are the box's, and need not exist on this machine.
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
	// Executable finds the program a command would run from cwd.
	Executable(ctx context.Context, program, cwd string) (string, error)
	// Same says two canonical paths are one file.
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
	// Transport carries the HTTP requests made for the user, such as to their MCP servers.
	Transport() http.RoundTripper
	Close() error
}

// Process is a command left running: Read takes its output, Write feeds its input, and Close, which may be called more
// than once, stops it. Read ends when the command does.
type Process interface {
	io.ReadWriteCloser
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

func (s *Sandbox) Transport() http.RoundTripper { return http.DefaultTransport }

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

func (s *Sandbox) Move(_ context.Context, from, to string) error {
	if err := os.Rename(from, to); err != nil {
		// Go refuses to rename onto a folder that is already there, even an empty one, and a user picks the folder
		// the workspace moves into: an empty one is replaced. Any other failure, such as a move across filesystems
		// or one the system does not allow, leaves the folder the user picked as it was.
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

// Here is a Box for reaching files on this machine with no rules of its own: what a user has when nothing else was
// given them. It runs no commands.
func Here() Box { return &Sandbox{} }

const stopDelay = 2 * time.Second

// moveTimeout is how long a machine's mv may take: a move of the user's whole folder, not a command of theirs.
const moveTimeout = 10 * time.Minute

func (s *Sandbox) Start(ctx context.Context, c Command, stderr io.Writer) (Process, error) {
	if c.Program == "" {
		return nil, errors.New("sandbox: incomplete command")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd, px, tagID, err := s.command(ctx, c)
	if err != nil {
		cancel()
		return nil, err
	}
	stop := func() {
		cancel()
		if px != nil {
			px.close()
		}
		if tagID != "" {
			s.denials.collect(tagID, false)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return nil, err
	}
	cmd.Stderr = stderr
	tag := runTag(s.runMarker())
	cmd.Env = append(cmd.Env, tag)
	ownProcessGroup(cmd, tag)
	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("sandbox: start %s: %w", c.Program, err)
	}
	return &process{ReadCloser: stdout, WriteCloser: stdin, cmd: cmd, stop: stop}, nil
}

// process is a command of this machine. Closing its input lets it end by itself; one that does not is killed.
type process struct {
	io.ReadCloser
	io.WriteCloser
	cmd  *exec.Cmd
	stop func()
	once sync.Once
}

func (p *process) Close() error {
	p.once.Do(func() {
		p.WriteCloser.Close()
		ended := make(chan struct{})
		go func() {
			p.cmd.Wait()
			close(ended)
		}()
		select {
		case <-ended:
			p.stop()
		case <-time.After(stopDelay):
			p.stop()
			<-ended
		}
	})
	return nil
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

// Remote makes a Box of a machine: its commands run in the first of roots, and files are written under them. summary
// tells the model what the machine allows. A nil machine only describes the tools.
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

// Move goes through the machine's own mv: its files are reached only through its commands. mv puts a folder inside a
// folder that is already there, so an empty target is removed first and to ends up holding what from held.
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

// run is one command of the machine, whose failure is an error of the move.
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

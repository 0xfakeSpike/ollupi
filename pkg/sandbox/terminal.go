package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
)

// A terminal is a person's own login shell in a folder of the box, as a
// terminal app gives one: theirs, not an agent's, so it runs with full
// access, as their own commands do, and asks no one.

// TerminalSpec is where a terminal starts and its size.
type TerminalSpec struct {
	Cwd  string
	Cols int
	Rows int
	// Env adds to or overrides the shell's environment.
	Env map[string]string
}

// Terminal is a running shell: what it prints is read, what the person
// types is written. Close ends it.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	// Wait waits for the shell to end and answers its exit code.
	Wait() (int, error)
}

var errNoTerminal = errors.New("sandbox: this machine offers no terminal")

// loginShell is the person's shell on this machine.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

// Terminal starts the person's login shell in spec.Cwd, with full access.
func (r *remote) Terminal(ctx context.Context, spec TerminalSpec) (Terminal, error) {
	if r.Machine == nil {
		return nil, errNoTerminal
	}
	return r.Machine.Terminal(ctx, spec)
}

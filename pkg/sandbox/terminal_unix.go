//go:build !windows

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Terminal starts the person's login shell in spec.Cwd under a pseudo
// terminal, with full access: under Seatbelt only to keep it from the
// folders no command reads (Config.DenyRead).
func (s *Sandbox) Terminal(_ context.Context, spec TerminalSpec) (Terminal, error) {
	env := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	for k, v := range spec.Env {
		env[k] = v
	}
	// The shell outlives the request that starts it: Close ends it.
	cmd, _, _, err := s.command(context.Background(), Command{Program: loginShell(), Args: []string{"-l"}, Cwd: spec.Cwd, Env: env, FullAccess: true})
	if err != nil {
		return nil, err
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(spec.Cols, 1)), Rows: uint16(max(spec.Rows, 1))})
	if err != nil {
		return nil, err
	}
	t := &ptyTerminal{File: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		t.err = cmd.Wait()
		close(t.done)
	}()
	return t, nil
}

type ptyTerminal struct {
	*os.File
	cmd   *exec.Cmd
	done  chan struct{}
	err   error
	close sync.Once
}

func (t *ptyTerminal) Resize(cols, rows int) error {
	return pty.Setsize(t.File, &pty.Winsize{Cols: uint16(max(cols, 1)), Rows: uint16(max(rows, 1))})
}

func (t *ptyTerminal) Wait() (int, error) {
	<-t.done
	var exit *exec.ExitError
	if errors.As(t.err, &exit) {
		return exit.ExitCode(), nil
	}
	if t.err != nil {
		return -1, t.err
	}
	return 0, nil
}

// Close hangs up on the shell as a closed terminal window does, and kills
// it if it is still there after a while.
func (t *ptyTerminal) Close() error {
	t.close.Do(func() {
		t.cmd.Process.Signal(syscall.SIGHUP)
		select {
		case <-t.done:
		case <-time.After(3 * time.Second):
			t.cmd.Process.Kill()
			<-t.done
		}
		t.File.Close()
	})
	return nil
}

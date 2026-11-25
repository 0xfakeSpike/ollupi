package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// A person's own terminals: a login shell per workspace, in its folder,
// where the workspace is. They are the person's, not the agent's: they run
// with full access, as the person's own commands do, and nothing in them
// asks for approval. A terminal belongs to the deployment, not to a
// client: it lives until its shell exits, its workspace is archived or its
// environment deleted, or the deployment stops; a client that closes or
// loses its connection attaches again and reads what it printed since it
// started (the last ScrollbackBytes of it).

// ScrollbackBytes is as much of a terminal's output as is kept to show
// again: a screenful of scrollback many times over.
const ScrollbackBytes = 256 << 10

var ErrNoTerminal = errors.New("no such terminal is running")

type terminals struct {
	mu sync.Mutex
	// byID are the running terminals; byPlace finds one by its user and
	// workspace.
	byID    map[string]*terminal
	byPlace map[[2]string]*terminal
}

type terminal struct {
	id, user, workspace string
	shell               sandbox.Terminal

	mu        sync.Mutex
	output    []byte
	listeners map[chan []byte]struct{}
	exited    chan struct{}
	code      int
}

// TerminalOutput is a terminal attached to: what it printed so far, what
// it prints next, and its end.
type TerminalOutput struct {
	Before []byte
	Next   <-chan []byte
	// Exited closes when the shell has ended; Code is its exit code then.
	Exited <-chan struct{}
	Code   func() int
	Detach func()
}

// OpenTerminal is the running terminal of a workspace of the user's, or
// a new one started in its folder at the size given.
func (a *App) OpenTerminal(ctx context.Context, user, workspace string, cols, rows int) (string, error) {
	a.terms.mu.Lock()
	t := a.terms.byPlace[[2]string{user, workspace}]
	a.terms.mu.Unlock()
	if t != nil {
		if err := t.shell.Resize(cols, rows); err != nil {
			slog.Debug("app: a terminal was not resized", "terminal", t.id, "error", err)
		}
		return t.id, nil
	}
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return "", err
	}
	if err := p.workspace.Usable(); err != nil {
		return "", err
	}
	shell, err := p.box.Terminal(ctx, sandbox.TerminalSpec{Cwd: p.root, Cols: cols, Rows: rows})
	if err != nil {
		return "", fmt.Errorf("the terminal of workspace %s: %w", p.workspace.Name, err)
	}
	t = &terminal{id: uuid.NewString(), user: user, workspace: workspace, shell: shell, listeners: map[chan []byte]struct{}{}, exited: make(chan struct{})}
	a.terms.mu.Lock()
	if other := a.terms.byPlace[[2]string{user, workspace}]; other != nil {
		// Another open started one meanwhile; it is the workspace's.
		a.terms.mu.Unlock()
		shell.Close()
		return other.id, nil
	}
	if a.terms.byID == nil {
		a.terms.byID, a.terms.byPlace = map[string]*terminal{}, map[[2]string]*terminal{}
	}
	a.terms.byID[t.id], a.terms.byPlace[[2]string{user, workspace}] = t, t
	a.terms.mu.Unlock()
	go a.pump(t)
	return t.id, nil
}

// pump reads what the shell prints, keeps the last of it and hands it to
// whoever is attached, until the shell ends.
func (a *App) pump(t *terminal) {
	buf := make([]byte, 32<<10)
	for {
		n, err := t.shell.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			t.mu.Lock()
			t.output = append(t.output, chunk...)
			if over := len(t.output) - ScrollbackBytes; over > 0 {
				t.output = append([]byte(nil), t.output[over:]...)
			}
			for l := range t.listeners {
				select {
				case l <- chunk:
				default:
					// A client that does not keep up is let go; it attaches
					// again and reads the scrollback.
					delete(t.listeners, l)
					close(l)
				}
			}
			t.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	code, err := t.shell.Wait()
	if err != nil {
		slog.Debug("app: a terminal's shell ended", "terminal", t.id, "error", err)
	}
	a.terms.mu.Lock()
	delete(a.terms.byID, t.id)
	if a.terms.byPlace[[2]string{t.user, t.workspace}] == t {
		delete(a.terms.byPlace, [2]string{t.user, t.workspace})
	}
	a.terms.mu.Unlock()
	t.shell.Close()
	t.mu.Lock()
	t.code = code
	for l := range t.listeners {
		delete(t.listeners, l)
		close(l)
	}
	t.mu.Unlock()
	close(t.exited)
}

func (a *App) terminal(user, id string) (*terminal, error) {
	a.terms.mu.Lock()
	defer a.terms.mu.Unlock()
	t := a.terms.byID[id]
	if t == nil || t.user != user {
		return nil, fmt.Errorf("terminal %s: %w", id, ErrNoTerminal)
	}
	return t, nil
}

// AttachTerminal follows a terminal of the user's: what it printed so far
// and, until Detach, what it prints next.
func (a *App) AttachTerminal(user, id string) (TerminalOutput, error) {
	t, err := a.terminal(user, id)
	if err != nil {
		return TerminalOutput{}, err
	}
	next := make(chan []byte, 256)
	t.mu.Lock()
	before := append([]byte(nil), t.output...)
	select {
	case <-t.exited:
		close(next)
	default:
		t.listeners[next] = struct{}{}
	}
	t.mu.Unlock()
	code := func() int {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.code
	}
	detach := func() {
		t.mu.Lock()
		if _, ok := t.listeners[next]; ok {
			delete(t.listeners, next)
			close(next)
		}
		t.mu.Unlock()
	}
	return TerminalOutput{Before: before, Next: next, Exited: t.exited, Code: code, Detach: detach}, nil
}

// WriteTerminal types into a terminal of the user's.
func (a *App) WriteTerminal(user, id string, data []byte) error {
	t, err := a.terminal(user, id)
	if err != nil {
		return err
	}
	_, err = t.shell.Write(data)
	return err
}

func (a *App) ResizeTerminal(user, id string, cols, rows int) error {
	t, err := a.terminal(user, id)
	if err != nil {
		return err
	}
	return t.shell.Resize(cols, rows)
}

// CloseTerminal ends a terminal of the user's.
func (a *App) CloseTerminal(user, id string) error {
	t, err := a.terminal(user, id)
	if err != nil {
		return err
	}
	return t.shell.Close()
}

// closeTerminals ends the terminals of the workspaces keep says.
func (a *App) closeTerminals(keep func(user, workspace string) bool) {
	a.terms.mu.Lock()
	var ending []*terminal
	for _, t := range a.terms.byID {
		if !keep(t.user, t.workspace) {
			ending = append(ending, t)
		}
	}
	a.terms.mu.Unlock()
	for _, t := range ending {
		t.shell.Close()
	}
}

// CloseTerminals ends every terminal, as the deployment stops.
func (a *App) CloseTerminals() {
	a.closeTerminals(func(string, string) bool { return false })
}

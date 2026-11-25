package gateway

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/auth"
)

// A person's terminal in a workspace: terminal/open starts it, or finds
// the one running, and the terminal path streams it. What the shell prints
// comes as binary messages, what it printed so far first; what the person
// types goes as binary or text messages. When the shell ends, the stream
// closes normally with the reason "exit <code>".

type TerminalOpenParams struct {
	WorkspaceID string `json:"workspaceId"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
}

type TerminalOpenResult struct {
	TerminalID string `json:"terminalId"`
}

type TerminalResizeParams struct {
	TerminalID string `json:"terminalId"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

type TerminalParams struct {
	TerminalID string `json:"terminalId"`
}

func (c *conn) terminalOpen(ctx context.Context, p TerminalOpenParams) (TerminalOpenResult, error) {
	id, err := c.s.app.OpenTerminal(ctx, c.user, p.WorkspaceID, p.Cols, p.Rows)
	return TerminalOpenResult{TerminalID: id}, err
}

func (c *conn) terminalResize(_ context.Context, p TerminalResizeParams) (Empty, error) {
	return Empty{}, c.s.app.ResizeTerminal(c.user, p.TerminalID, p.Cols, p.Rows)
}

func (c *conn) terminalClose(_ context.Context, p TerminalParams) (Empty, error) {
	return Empty{}, c.s.app.CloseTerminal(c.user, p.TerminalID)
}

// TerminalPath streams a terminal of the connection's user.
const TerminalPath = "/terminals/{terminal}"

const maxTerminalInputBytes = 1 << 20

func (s *Server) terminalStream(w http.ResponseWriter, r *http.Request) {
	s.served.Add(1)
	defer s.served.Done()
	id, ok := s.authorize(w, r)
	if !ok || !s.serves(w, r, id.User) {
		return
	}
	terminal := r.PathValue("terminal")
	out, err := s.app.AttachTerminal(id.User, terminal)
	if errors.Is(err, app.ErrNoTerminal) {
		http.Error(w, "no such terminal is running", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "the terminal cannot be reached now", http.StatusInternalServerError)
		return
	}
	defer out.Detach()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins, Subprotocols: []string{auth.TokenProtocol}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxTerminalInputBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if err := s.app.WriteTerminal(id.User, terminal, data); err != nil {
				return
			}
		}
	}()
	if len(out.Before) > 0 {
		if err := ws.Write(ctx, websocket.MessageBinary, out.Before); err != nil {
			return
		}
	}
	// A ping now and then keeps an idle terminal's connection through
	// proxies that drop a quiet one.
	ping := time.NewTicker(DefaultConnections.PingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			go ws.Ping(ctx)
		case chunk, open := <-out.Next:
			if open {
				if err := ws.Write(ctx, websocket.MessageBinary, chunk); err != nil {
					return
				}
				continue
			}
			select {
			case <-out.Exited:
				ws.Close(websocket.StatusNormalClosure, "exit "+strconv.Itoa(out.Code()))
			default:
				// Let go for not keeping up: attaching again reads the
				// scrollback.
				ws.Close(websocket.StatusTryAgainLater, "the terminal printed faster than it was read")
			}
			return
		}
	}
}

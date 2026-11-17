package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type handlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, func(), *Error)

func (f handlerFunc) Call(ctx context.Context, method string, params json.RawMessage) (any, func(), *Error) {
	return f(ctx, method, params)
}

func (handlerFunc) Notify(string, json.RawMessage) {}

func serve(ctx context.Context, t *testing.T, h func(*Conn) Handler) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- ws
		c := NewConn(ws)
		c.Serve(r.Context(), h(c))
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.CloseNow() })
	return <-accepted, client
}

func write(ctx context.Context, t *testing.T, ws *websocket.Conn, msg string) {
	t.Helper()
	if err := ws.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func next(ctx context.Context, t *testing.T, ws *websocket.Conn) Message {
	t.Helper()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAPeerIsHeardWhileACallIsSlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	release := make(chan struct{})
	server, client := serve(ctx, t, func(*Conn) Handler {
		return handlerFunc(func(context.Context, string, json.RawMessage) (any, func(), *Error) {
			<-release
			return "done", nil, nil
		})
	})
	write(ctx, t, client, `{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	write(ctx, t, client, `{"jsonrpc":"2.0","id":2,"method":"slow"}`)
	replies := make(chan string, 2)
	go func() {
		for range 2 {
			_, data, err := client.Read(ctx)
			if err != nil {
				return
			}
			var m Message
			json.Unmarshal(data, &m)
			replies <- string(m.ID)
		}
	}()

	pingCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if err := server.Ping(pingCtx); err != nil {
		t.Errorf("the pong to a ping sent during a slow call was not read: %v", err)
	}
	close(release)
	for _, want := range []string{"1", "2"} {
		select {
		case got := <-replies:
			if got != want {
				t.Errorf("reply to %s came for %s", want, got)
			}
		case <-ctx.Done():
			t.Fatal("the replies did not come")
		}
	}
}

func TestWhatIsSentWhileACallHoldsComesAfterItsReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, client := serve(ctx, t, func(c *Conn) Handler {
		return handlerFunc(func(_ context.Context, method string, _ json.RawMessage) (any, func(), *Error) {
			if method == "view" {
				c.HoldUntilReplied()
			}
			c.Notify("changed", nil)
			return "view", nil, nil
		})
	})
	write(ctx, t, client, `{"jsonrpc":"2.0","id":1,"method":"view"}`)
	if m := next(ctx, t, client); string(m.ID) != "1" {
		t.Errorf("first came %+v, want the reply", m)
	}
	if m := next(ctx, t, client); m.Method != "changed" {
		t.Errorf("then came %+v, want the held notification", m)
	}

	write(ctx, t, client, `{"jsonrpc":"2.0","id":2,"method":"plain"}`)
	if m := next(ctx, t, client); m.Method != "changed" {
		t.Errorf("a call that does not hold: first came %+v", m)
	}
	if m := next(ctx, t, client); string(m.ID) != "2" {
		t.Errorf("a call that does not hold: then came %+v", m)
	}
}

func TestAReplyLargerThanTheOutboxGoesOutAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, client := serve(ctx, t, func(*Conn) Handler {
		return handlerFunc(func(_ context.Context, method string, _ json.RawMessage) (any, func(), *Error) {
			if method == "big" {
				return strings.Repeat("a", maxOutboxBytes+1), nil, nil
			}
			return "ok", nil, nil
		})
	})
	client.SetReadLimit(2 * maxOutboxBytes)
	write(ctx, t, client, `{"jsonrpc":"2.0","id":1,"method":"big"}`)
	var big string
	if m := next(ctx, t, client); m.Error != nil || json.Unmarshal(m.Result, &big) != nil || len(big) != maxOutboxBytes+1 {
		t.Fatalf("the large reply: %d bytes, %v", len(big), m.Error)
	}
	write(ctx, t, client, `{"jsonrpc":"2.0","id":2,"method":"small"}`)
	if m := next(ctx, t, client); m.Error != nil || string(m.Result) != `"ok"` {
		t.Fatalf("after the large reply: %s, %v", m.Result, m.Error)
	}
}

func TestAFloodingClientIsReadAtItsPace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, client := serve(ctx, t, func(*Conn) Handler {
		return handlerFunc(func(context.Context, string, json.RawMessage) (any, func(), *Error) { return "ok", nil, nil })
	})
	const sent = messageBurst + messagesPerSecond
	go func() {
		for i := range sent {
			if client.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"m"}`, i))) != nil {
				return
			}
		}
	}()
	started := time.Now()
	for range sent {
		next(ctx, t, client)
	}
	if elapsed := time.Since(started); elapsed < 800*time.Millisecond {
		t.Errorf("%d calls were answered in %s, past the pace of %d a second after %d", sent, elapsed, messagesPerSecond, messageBurst)
	}
}

package cloudflare

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// bridge is the part of a Cloudflare sandbox bridge these tests need: a
// sandbox or two, exec's events, and files in memory.
type bridge struct {
	mu      sync.Mutex
	next    int
	files   map[string][]byte
	deleted []string
	execs   [][]string
}

func (b *bridge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /v1/sandbox", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.next++
		id := fmt.Sprintf("sb-%d", b.next)
		b.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("DELETE /v1/sandbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.deleted = append(b.deleted, r.PathValue("id"))
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/sandbox/{id}/exec", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Argv []string `json:"argv"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		b.mu.Lock()
		b.execs = append(b.execs, in.Argv)
		b.mu.Unlock()
		stdout, stderr, exit := "", "", 0
		switch in.Argv[0] {
		case "stat":
			stdout = "directory\t0\t1700000000\t755\n"
		case "find":
			stdout = "a.txt\tf\t3\t1700000000.5\nsub\td\t0\t1700000001.0\n"
		case "echo":
			if strings.Join(in.Argv, " ") == "echo hi" {
				stdout = "hi\n"
			}
		case "false":
			exit = 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []struct{ event, data string }{{"stdout", stdout}, {"stderr", stderr}} {
			if e.data == "" {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, base64.StdEncoding.EncodeToString([]byte(e.data)))
		}
		fmt.Fprintf(w, "event: exit\ndata: {\"exit_code\":%d}\n\n", exit)
	})
	mux.HandleFunc("GET /v1/sandbox/{id}/running", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"running": true})
	})
	mux.HandleFunc("PUT /v1/sandbox/{id}/file/{path...}", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.files[r.PathValue("path")] = data
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/sandbox/{id}/file/{path...}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		data, ok := b.files[r.PathValue("path")]
		b.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /v1/sandbox/{id}/pty", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`))
		_ = conn.Write(ctx, websocket.MessageBinary, []byte("hello"))
		if _, _, err := conn.Read(ctx); err == nil {
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","code":0}`))
		}
	})
	return mux
}

func newProvider(t *testing.T, b *bridge) *Provider {
	t.Helper()
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func open(t *testing.T, p *Provider, state *string) sandbox.Box {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var kept string
	box, err := p.Open(ctx, sandbox.Env{User: "u", ID: "e", State: func() json.RawMessage {
		if state == nil || *state == "" {
			return nil
		}
		return json.RawMessage(*state)
	}(), Keep: func(raw json.RawMessage) error { kept = string(raw); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		*state = kept
	}
	return box
}

func TestRunGivesTheCommandAndItsEvents(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	res, err := box.Run(context.Background(), sandbox.Command{Program: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hi\n" || res.ExitCode != 0 {
		t.Fatalf("got %+v", res)
	}
	if len(b.execs) == 0 || strings.Join(b.execs[len(b.execs)-1], " ") != "echo hi" {
		t.Fatalf("execs = %v", b.execs)
	}
}

func TestFilesGoThroughTheBridge(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	ctx := context.Background()
	if err := box.WriteFile(ctx, "/workspace/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := box.Open(ctx, "/workspace/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read %q, %v", data, err)
	}
}

func TestStatAndReadDirAskTheSandbox(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	ctx := context.Background()
	st, err := box.Stat(ctx, "/workspace/dir")
	if err != nil || !st.IsDir() {
		t.Fatalf("stat = %+v, %v", st, err)
	}
	entries, err := box.ReadDir(ctx, "/workspace")
	if err != nil || len(entries) != 2 || entries[0].Name() != "a.txt" || !entries[1].IsDir() {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
}

func TestAnEnvironmentKeepsItsSandbox(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	var state string
	open(t, p, &state)
	if state == "" || !strings.Contains(state, "sb-1") || b.next != 1 {
		t.Fatalf("state = %q, created %d", state, b.next)
	}
	open(t, p, &state)
	if b.next != 1 {
		t.Fatalf("a second Open made %d sandboxes", b.next)
	}
}

func TestRemoveDeletesTheSandbox(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	var state string
	open(t, p, &state)
	if err := p.Remove(context.Background(), sandbox.Env{State: json.RawMessage(state)}); err != nil {
		t.Fatal(err)
	}
	if len(b.deleted) != 1 || b.deleted[0] != "sb-1" {
		t.Fatalf("deleted = %v", b.deleted)
	}
}

func TestFullAccessIsAllowed(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	res, err := box.Run(context.Background(), sandbox.Command{Program: "echo", Args: []string{"hi"}, FullAccess: true})
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !box.CanEscalate() {
		t.Fatal("the box refuses to escalate")
	}
}

func TestTheHubFolderStaysInsideTheWorkspace(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	dir, err := box.Folder(context.Background(), "super-agent")
	if err != nil || dir != "/workspace/.ollupi/super-agent" {
		t.Fatalf("folder = %q, %v", dir, err)
	}
}

func TestTerminalSpeaksTheBridgePTY(t *testing.T) {
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	box := open(t, p, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	term, err := box.Terminal(ctx, sandbox.TerminalSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(term, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if _, err := term.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	code, err := term.Wait()
	if err != nil || code != 0 {
		t.Fatalf("wait = %d, %v", code, err)
	}
	_ = term.Close()
}

func TestTheProviderChecksItsSettings(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("a provider without a URL was made")
	}
	b := &bridge{files: map[string][]byte{}}
	p := newProvider(t, b)
	if err := p.Check(nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(json.RawMessage(`{"x":1}`)); err == nil {
		t.Fatal("settings were taken")
	}
}

package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// TestLiveBridge is the check to run where the bridge is reachable: it makes
// a sandbox, runs a command, moves a file through the file route, and opens
// the PTY. It is skipped unless SANDBOX_API_URL and SANDBOX_API_KEY are set.
func TestLiveBridge(t *testing.T) {
	address, key := os.Getenv("SANDBOX_API_URL"), os.Getenv("SANDBOX_API_KEY")
	if address == "" || key == "" {
		t.Skip("set SANDBOX_API_URL and SANDBOX_API_KEY to run against a live bridge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := New(Config{BaseURL: address, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	// Where DNS for the bridge is poisoned, SANDBOX_API_IP names the address
	// to dial instead, at the port the URL names (80 for http, 443 for https).
	if ip := os.Getenv("SANDBOX_API_IP"); ip != "" {
		u, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		dialer := &net.Dialer{}
		p.client.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(addr); err == nil && host == u.Hostname() {
				addr = net.JoinHostPort(ip, port)
			}
			return dialer.DialContext(ctx, network, addr)
		}}}
		t.Logf("dialing %s as %s", u.Hostname(), net.JoinHostPort(ip, port))
	}
	var kept json.RawMessage
	box, err := p.Open(ctx, sandbox.Env{User: "live", ID: "one", Keep: func(raw json.RawMessage) error { kept = raw; return nil }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if len(kept) > 0 {
			if err := p.Remove(context.Background(), sandbox.Env{State: kept}); err != nil {
				t.Logf("remove: %v", err)
			}
		}
	}()

	res, err := box.Run(ctx, sandbox.Command{Program: "sh", Args: []string{"-lc", "echo $0; uname -s"}, Timeout: 30 * time.Second})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exec: %+v, %v", res, err)
	}
	t.Logf("exec: %s%s", res.Stdout, res.Stderr)

	if err := box.WriteFile(ctx, "/workspace/live.txt", []byte("hello from ollupi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := box.Open(ctx, "/workspace/live.txt")
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	body, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(body) != "hello from ollupi\n" {
		t.Fatalf("read file: %q, %v", body, err)
	}
	if st, err := box.Stat(ctx, "/workspace/live.txt"); err != nil || st.Size() != int64(len(body)) {
		t.Fatalf("stat: %+v, %v", st, err)
	}
	if entries, err := box.ReadDir(ctx, "/workspace"); err != nil {
		t.Fatalf("readdir: %v", err)
	} else {
		t.Logf("workspace holds %d entries", len(entries))
	}

	term, err := box.Terminal(ctx, sandbox.TerminalSpec{Cwd: "/workspace", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("terminal: %v", err)
	}
	defer term.Close()
	if _, err := term.Write([]byte("echo live-pty\n")); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	out := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var seen strings.Builder
		for {
			n, err := term.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if strings.Contains(seen.String(), "live-pty") {
					out <- seen.String()
					return
				}
			}
			if err != nil {
				out <- seen.String()
				return
			}
		}
	}()
	select {
	case got := <-out:
		t.Logf("pty: %q", got)
	case <-time.After(15 * time.Second):
		t.Fatal("the terminal printed nothing within 15s")
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
}

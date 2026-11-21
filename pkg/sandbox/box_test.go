package sandbox

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartLeavesACommandRunning(t *testing.T) {
	modes := []string{"full-access"}
	if runtime.GOOS == "darwin" {
		modes = append(modes, "workspace-write")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			s, err := New(Config{Mode: mode, Roots: []string{root}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var stderr strings.Builder
			p, err := s.Start(context.Background(), Command{Program: "/bin/sh", Args: []string{"-c", "echo up >&2; cat"}, Cwd: root}, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Write([]byte("hello\n")); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(p).ReadString('\n')
			if err != nil || line != "hello\n" {
				t.Errorf("read %q, %v", line, err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Errorf("closing twice: %v", err)
			}
			if !strings.Contains(stderr.String(), "up") {
				t.Errorf("stderr %q", stderr.String())
			}
		})
	}
}

func TestCloseStopsACommandThatWillNotEnd(t *testing.T) {
	s, err := New(Config{Mode: "full-access", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Start(context.Background(), Command{Program: "/bin/sleep", Args: []string{"60"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	p.Close()
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("stopping took %s", took)
	}
}

func TestNoHomeIsAnError(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := New(Config{Mode: "workspace-write", Roots: []string{t.TempDir()}}); err == nil {
		t.Error("the rules that protect the home directory were left out")
	}
}

func TestARemoteBoxKeepsOffThisNetwork(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer local.Close()
	box := Remote(nil, []string{"/cloud"}, "cloud")
	defer box.Close()
	req, err := http.NewRequest(http.MethodGet, local.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := box.Transport().RoundTrip(req); !errors.Is(err, ErrLocalAddress) {
		t.Errorf("reached %s: %v, %v", local.URL, res, err)
	}
}

func TestTheMachinesRequestsTakeItsProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:15236")
	box, err := Here()
	if err != nil {
		t.Fatal(err)
	}
	proxy := box.Transport().(*http.Transport).Proxy
	for raw, want := range map[string]string{"https://github.com/": "http://127.0.0.1:15236", "https://localhost:8443/": ""} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		u, err := proxy(req)
		got := ""
		if u != nil {
			got = u.String()
		}
		if err != nil || got != want {
			t.Errorf("%s goes through %q (%v), want %q", raw, got, err, want)
		}
	}
}

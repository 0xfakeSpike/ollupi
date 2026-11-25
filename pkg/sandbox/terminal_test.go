//go:build !windows

package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestATerminalIsTheLoginShellWithFullAccessButTheWithheld(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	data, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(data, "store"), []byte("keys"), 0o600)
	mode := "full-access"
	if runtime.GOOS == "darwin" {
		mode = "workspace-write"
	}
	s, err := New(Config{Mode: mode, Roots: []string{root}, DenyRead: []string{data}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	term, err := s.Terminal(context.Background(), TerminalSpec{Cwd: root, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if err := term.Resize(120, 40); err != nil {
		t.Errorf("resize: %v", err)
	}
	script := "pwd; echo ok-$((1+1)) > made.txt; cat made.txt\n"
	if runtime.GOOS == "darwin" {
		script = "cat " + filepath.Join(data, "store") + " || echo withheld-$((1+1))\n" + script
	}
	term.Write([]byte(script + "exit 7\n"))
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("the shell did not end: %q", out.String())
	}
	code, err := term.Wait()
	if err != nil || code != 7 {
		t.Errorf("the shell ended with %d, %v", code, err)
	}
	text := out.String()
	if !strings.Contains(text, root) || !strings.Contains(text, "ok-2") {
		t.Errorf("the shell printed %q", text)
	}
	if runtime.GOOS == "darwin" && (strings.Contains(text, "keys") || !strings.Contains(text, "withheld-2")) {
		t.Errorf("the shell read the withheld folder: %q", text)
	}
}

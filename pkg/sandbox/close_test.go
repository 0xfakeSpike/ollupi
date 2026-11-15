//go:build unix

package sandbox

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCloseEndsWhatTheCommandLeftRunning(t *testing.T) {
	s, err := New(Config{Mode: "full-access", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var stderr strings.Builder
	// The child keeps the command's stderr open after the command ends, as a server's browser can.
	p, err := s.Start(context.Background(), Command{Program: "/bin/sh", Args: []string{"-c", "sleep 60 & echo $!; cat"}}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close waits for the child that holds stderr")
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the child still runs after Close")
		}
	}
}

package mcp

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverMachine is a user's cloud sandbox: a command started in it is an MCP server that says whose machine it is,
// reached over a pipe, with no process on this machine.
type serverMachine struct {
	sandbox.Machine
	user    string
	started atomic.Int32
	mu      sync.Mutex
	last    net.Conn
	ran     sandbox.Command
}

func (m *serverMachine) Start(ctx context.Context, c sandbox.Command, _ io.Writer) (sandbox.Process, error) {
	ours, theirs := net.Pipe()
	s := mcp.NewServer(&mcp.Implementation{Name: "in-the-machine", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "whose", Description: "Says whose machine this is."}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: m.user + " " + c.Env["TOKEN"]}}}, nil, nil
	})
	go s.Run(ctx, &mcp.IOTransport{Reader: theirs, Writer: theirs})
	m.started.Add(1)
	m.mu.Lock()
	m.last, m.ran = theirs, c
	m.mu.Unlock()
	return ours, nil
}

func (m *serverMachine) Close() error { return nil }

func TestEachUsersServersRunInTheirMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	machines := map[string]*serverMachine{"a": {user: "a"}, "b": {user: "b"}, "c": {user: "c"}}
	users := NewUsers(ctx, Client{}, func(user string) ([]Server, error) {
		if user == "c" {
			return nil, nil
		}
		return []Server{{Name: "own", Command: "mcp-server", Args: []string{"--stdio"}, Env: map[string]string{"TOKEN": "of-" + user}}}, nil
	}, func(user string) (sandbox.Box, error) {
		return sandbox.Remote(machines[user], []string{"/cloud/work"}, "cloud"), nil
	})
	defer users.Close()

	call := func(user string) (*tools.Set, string) {
		t.Helper()
		set, err := users.Tools(user)
		if err != nil {
			t.Fatal(err)
		}
		reg, err := tools.New(ctx, nil, set.Providers...)
		if err != nil {
			t.Fatal(err)
		}
		r := reg.Invoke(ctx, "own__whose", map[string]any{})
		if r.Err != nil {
			t.Fatalf("%s: %v", user, r.Err)
		}
		return set, r.Output.(string)
	}
	if machines["a"].started.Load() != 0 {
		t.Error("a server started before its user needed it")
	}
	setA, got := call("a")
	if got != "a of-a" {
		t.Errorf("a reached %q", got)
	}
	if _, got := call("b"); got != "b of-b" {
		t.Errorf("b reached %q", got)
	}
	if ran := machines["a"].ran; ran.Program != "mcp-server" || ran.Cwd != "/cloud/work" || len(ran.Args) != 1 {
		t.Errorf("a's machine ran %+v", ran)
	}
	if again, _ := call("a"); again != setA || machines["a"].started.Load() != 1 {
		t.Errorf("a second call: the same tools %v, started %d", again == setA, machines["a"].started.Load())
	}
	if set, err := users.Tools("c"); err != nil || len(set.Providers) != 0 {
		t.Errorf("c has no servers: %+v, %v", set, err)
	}

	// The sandbox slept: its process is gone, and the next call starts it again.
	m := machines["a"]
	m.mu.Lock()
	m.last.Close()
	m.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		set, err := users.Tools("a")
		if err != nil {
			t.Fatal(err)
		}
		if set != setA {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dropped server never came back")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, got := call("a"); got != "a of-a" || m.started.Load() != 2 {
		t.Errorf("after coming back: %q, started %d", got, m.started.Load())
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	switch os.Getenv("OLLUPI_MCP_TEST_SERVER") {
	case "serve":
		if err := testServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "missing API token")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func testServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1"}, nil)
	greet := func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello " + os.Getenv("GREETING")}}}, nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "greet", Description: "Greets."}, greet)
	mcp.AddTool(s, &mcp.Tool{Name: "a.dotted.name", Description: "Has a name the model API refuses."}, greet)
	s.AddTool(&mcp.Tool{Name: "lookahead", Description: "Has a pattern the registry cannot compile.", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string", "pattern": "^(?!x).*$"}},
	}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Tells who called."}, func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		raw, err := json.Marshal(req.Params.Meta[metaCaller])
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, err
	})
	return s
}

func newUsers(t *testing.T, ctx context.Context, configs map[string][]user.MCPServer, boxes func(string) (sandbox.Box, error)) (*Users, store.Store) {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	err = st.Update(ctx, func(tx store.Tx) error {
		for u, servers := range configs {
			if err := tx.EnsureUser(u); err != nil {
				return err
			}
			for _, s := range servers {
				if err := tx.PutMCPServer(u, s); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewUsers(ctx, Client{Name: "ollupi", Version: "test"}, st, boxes), st
}

func invoke(set tool.Set, name string, caller tool.Caller) (string, string) {
	r := set.Invoke(context.Background(), tool.Call{Caller: caller, Tool: name, Arguments: map[string]any{}})
	var text string
	if json.Unmarshal(r.Output, &text) != nil {
		text = string(r.Output)
	}
	return text, r.Error
}

func statuses(t *testing.T, users *Users, name string) map[string]user.MCPStatus {
	t.Helper()
	list, err := users.Statuses(name)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]user.MCPStatus{}
	for _, st := range list {
		out[st.Name] = st
	}
	return out
}

func TestConnect(t *testing.T) {
	ctx := context.Background()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GREETING", "from the parent")
	t.Setenv("OLLUPI_MCP_TEST_SERVER", "")

	authorized := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer() }, nil)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authorized.ServeHTTP(w, r)
	}))
	defer web.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := "http://" + closed.Addr().String()
	closed.Close()

	box, err := sandbox.New(sandbox.Config{Mode: "full-access", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	users, _ := newUsers(t, ctx, map[string][]user.MCPServer{"someone": {
		{Name: "local", Command: self, Env: map[string]string{"OLLUPI_MCP_TEST_SERVER": "serve", "GREETING": "from config"}},
		{Name: "web", URL: web.URL, Headers: map[string]string{"Authorization": "Bearer secret"}},
		{Name: "no-header", URL: web.URL},
		{Name: "crashes", Command: self, Env: map[string]string{"OLLUPI_MCP_TEST_SERVER": "fail"}},
		{Name: "gone", URL: unreachable},
	}}, func(string) (sandbox.Box, error) { return box, nil })
	defer users.Close()

	byName := statuses(t, users, "someone")
	for _, name := range []string{"local", "web"} {
		st := byName[name]
		if !st.Connected || st.Tools != 2 || !slices.Equal(st.Skipped, []string{"a.dotted.name", "lookahead"}) || st.Error != "" {
			t.Errorf("%s: %+v", name, st)
		}
	}
	for name, why := range map[string]string{"no-header": "", "crashes": "missing API token", "gone": ""} {
		st := byName[name]
		if st.Connected || st.Error == "" || !strings.Contains(st.Error, why) {
			t.Errorf("%s: %+v", name, st)
		}
		if strings.Contains(st.Error, "secret") {
			t.Errorf("%s: the status leaks a header value: %s", name, st.Error)
		}
	}

	set, err := users.Tools(ctx, "someone")
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Unavailable(); !slices.Equal(got, []string{"crashes", "gone", "no-header"}) {
		t.Errorf("unavailable %v", got)
	}
	for name, want := range map[string]string{"local__greet": "hello from config", "web__greet": "hello from the parent"} {
		if out, err := invoke(set, name, tool.Caller{}); err != "" || out != want {
			t.Errorf("%s: %v, %v; want %q", name, out, err, want)
		}
	}
	for _, tc := range []struct {
		caller tool.Caller
		want   string
	}{
		{tool.Caller{}, "null"},
		{tool.Caller{Thread: "t1", Workspace: "w1", Root: "/home/me/project"}, `{"threadId":"t1","workspaceId":"w1"}`},
		{tool.Caller{Thread: "t2", Subagent: true}, `{"subagent":true,"threadId":"t2"}`},
	} {
		if out, err := invoke(set, "local__whoami", tc.caller); err != "" || out != tc.want {
			t.Errorf("caller %+v reached the server as %v, %v; want %s", tc.caller, out, err, tc.want)
		}
	}
}

func TestACallThatGetsNoAnswerEnds(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	hanging := mcp.NewServer(&mcp.Implementation{Name: "hanging", Version: "1"}, nil)
	mcp.AddTool(hanging, &mcp.Tool{Name: "hang"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		<-release
		return &mcp.CallToolResult{}, nil, nil
	})
	web := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return hanging }, nil))
	defer web.Close()
	defer close(release)
	client := mcp.NewClient(&mcp.Implementation{Name: "ollupi", Version: "test"}, nil)
	sess, err := connect(ctx, ctx, client, sandbox.Here(), Server{MCPServer: user.MCPServer{Name: "slow", URL: web.URL}, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.Invoke(ctx, tool.Call{Tool: "slow__hang", Arguments: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "may still have taken effect") {
		t.Errorf("the call: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		sess.sess.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * closeTimeout):
		t.Error("closing waits on the server's hung call")
	}
}

func TestHeadersStayWithTheirServer(t *testing.T) {
	var mu sync.Mutex
	reached, leaked := false, ""
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer() }, nil)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = true
		leaked += r.Header.Get("Authorization")
		mu.Unlock()
		mcpHandler.ServeHTTP(w, r)
	}))
	defer other.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	users, _ := newUsers(t, context.Background(), map[string][]user.MCPServer{"someone": {
		{Name: "moved", URL: front.URL, Headers: map[string]string{"Authorization": "Bearer secret"}},
	}}, func(string) (sandbox.Box, error) { return sandbox.Here(), nil })
	defer users.Close()
	if _, err := users.Tools(context.Background(), "someone"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reached || leaked != "" {
		t.Errorf("the other host was reached %v and got %q", reached, leaked)
	}
}

func TestARemoteUserCannotReachThisNetwork(t *testing.T) {
	web := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer() }, nil))
	defer web.Close()
	users, _ := newUsers(t, context.Background(), map[string][]user.MCPServer{"someone": {{Name: "internal", URL: web.URL}}},
		func(string) (sandbox.Box, error) { return sandbox.Remote(nil, []string{"/cloud"}, "cloud"), nil })
	defer users.Close()
	if st := statuses(t, users, "someone")["internal"]; st.Connected || !strings.Contains(st.Error, sandbox.ErrLocalAddress.Error()) {
		t.Errorf("%+v", st)
	}
}

func TestContentThatIsNotTextIsNamedNotDumped(t *testing.T) {
	got := joinTextContent([]mcp.Content{
		&mcp.TextContent{Text: "done"},
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///a.txt", Text: "inside"}},
	})
	if got != "done\n[image: image/png, 3 bytes, not shown]\ninside" {
		t.Errorf("%q", got)
	}
}

func TestAServersWordDoesNotMakeAToolReadOnly(t *testing.T) {
	closed := false
	spec := toSpec("srv__send", &mcp.Tool{Name: "send", Annotations: &mcp.ToolAnnotations{Title: "Send", ReadOnlyHint: true, OpenWorldHint: &closed}})
	if a := spec.Annotations; a.ReadOnlyHint || !a.OpenWorld() || a.Title != "Send" {
		t.Errorf("annotations %+v, want the title alone", a)
	}
}

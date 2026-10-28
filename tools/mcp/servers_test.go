package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
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
	return s
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

	servers := Connect(ctx, Client{}, []Server{
		{Name: "local", Command: self, Env: map[string]string{"OLLUPI_MCP_TEST_SERVER": "serve", "GREETING": "from config"}},
		{Name: "web", URL: web.URL, Headers: map[string]string{"Authorization": "Bearer secret"}},
		{Name: "no-header", URL: web.URL},
		{Name: "crashes", Command: self, Env: map[string]string{"OLLUPI_MCP_TEST_SERVER": "fail"}},
		{Name: "gone", URL: unreachable},
		{Name: "web", URL: web.URL},
		{Name: "a__b", URL: web.URL},
		{Name: "both", Command: self, URL: web.URL},
	})
	defer servers.Close()

	statuses := servers.Statuses()
	byName := map[string]ServerStatus{}
	for i, st := range statuses {
		if _, dup := byName[st.Name]; !dup {
			byName[st.Name] = st
		} else if st.Connected || st.Error == "" || i != 5 {
			t.Errorf("the second server named web: %+v", st)
		}
	}
	for _, name := range []string{"local", "web"} {
		st := byName[name]
		if !st.Connected || st.Tools != 1 || !slices.Equal(st.SkippedTools, []string{"a.dotted.name"}) || st.Error != "" {
			t.Errorf("%s: %+v", name, st)
		}
	}
	for name, why := range map[string]string{"no-header": "", "crashes": "missing API token", "gone": "", "a__b": "name", "both": "either"} {
		st := byName[name]
		if st.Connected || st.Error == "" || !strings.Contains(st.Error, why) {
			t.Errorf("%s: %+v", name, st)
		}
		if strings.Contains(st.Error, "secret") {
			t.Errorf("%s: the status leaks a header value: %s", name, st.Error)
		}
	}

	if got, want := servers.Unavailable(), []string{"no-header", "crashes", "gone", "both"}; !slices.Equal(got, want) {
		t.Errorf("unavailable servers %q, want %q", got, want)
	}

	providers := servers.Providers()
	if len(providers) != 2 {
		t.Fatalf("%d providers, want the 2 that connected", len(providers))
	}
	reg, err := tools.New(ctx, servers.Unavailable(), providers...)
	if err != nil {
		t.Fatal(err)
	}
	for tool, want := range map[string]string{"local__greet": "hello from config", "web__greet": "hello from the parent"} {
		r := reg.Invoke(ctx, tool, map[string]any{})
		if r.Err != nil || r.Output != want {
			t.Errorf("%s: %v, %v; want %q", tool, r.Output, r.Err, want)
		}
	}
}

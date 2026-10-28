package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Session struct {
	name    string
	title   string
	sess    *mcp.ClientSession
	tools   map[string]*mcp.Tool
	specs   []*tools.Spec
	skipped []string
	stderr  *tail

	mu   sync.Mutex
	down error
}

func connect(ctx context.Context, client *mcp.Client, s Server) (*Session, error) {
	out := &Session{name: s.Name, tools: map[string]*mcp.Tool{}}
	var transport mcp.Transport
	if s.Command != "" {
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = cmd.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out.stderr = &tail{}
		cmd.Stderr = out.stderr
		transport = &mcp.CommandTransport{Command: cmd}
	} else {
		httpClient := http.DefaultClient
		if len(s.Headers) > 0 {
			httpClient = &http.Client{Transport: headers{base: http.DefaultTransport, values: s.Headers}}
		}
		transport = &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: httpClient}
	}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, out.withStderr(err)
	}
	out.sess = sess
	if info := sess.InitializeResult().ServerInfo; info != nil {
		out.title = info.Name
	}
	for t, err := range sess.Tools(ctx, nil) {
		if err != nil {
			sess.Close()
			return nil, out.withStderr(fmt.Errorf("list tools: %w", err))
		}
		name := s.Name + tools.ProviderSeparator + t.Name
		if !toolName.MatchString(name) {
			out.skipped = append(out.skipped, t.Name)
			continue
		}
		out.tools[name] = t
		out.specs = append(out.specs, toSpec(name, t))
	}
	go func() {
		err := sess.Wait()
		out.mu.Lock()
		out.down = out.withStderr(fmt.Errorf("disconnected: %v", err))
		out.mu.Unlock()
	}()
	return out, nil
}

func (s *Session) withStderr(err error) error {
	if s.stderr == nil {
		return err
	}
	if text := s.stderr.String(); text != "" {
		return fmt.Errorf("%w; stderr: %s", err, text)
	}
	return err
}

func (s *Session) Name() (string, string) {
	if s.title != "" && s.title != s.name {
		return s.name, fmt.Sprintf("Tools from the MCP server %q (%s).", s.name, s.title)
	}
	return s.name, fmt.Sprintf("Tools from the MCP server %q.", s.name)
}

func (s *Session) ToolList(context.Context) ([]*tools.Spec, error) {
	return s.specs, nil
}

func toSpec(name string, t *mcp.Tool) *tools.Spec {
	spec := &tools.Spec{Name: name, Description: t.Description, InputSchema: t.InputSchema}
	if t.Annotations != nil {
		spec.Annotations = &tools.Annotations{
			Title:           t.Annotations.Title,
			ReadOnlyHint:    t.Annotations.ReadOnlyHint,
			DestructiveHint: t.Annotations.DestructiveHint,
			IdempotentHint:  t.Annotations.IdempotentHint,
			OpenWorldHint:   t.Annotations.OpenWorldHint,
		}
	}
	return spec
}

func (s *Session) Invoke(ctx context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	t, ok := s.tools[tool]
	if !ok {
		err := fmt.Errorf("mcp: tool %q not exposed by MCP server %q", tool, s.name)
		return tools.Result{Err: err}, err
	}
	res, err := s.sess.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: arguments})
	if err != nil {
		return tools.Result{Err: err}, err
	}
	if res.IsError {
		msg := joinTextContent(res.Content)
		if msg == "" {
			msg = "tool error"
		}
		return tools.Result{Output: msg, Err: errors.New(msg)}, nil
	}
	if res.StructuredContent != nil {
		return tools.Result{Output: res.StructuredContent}, nil
	}
	return tools.Result{Output: joinTextContent(res.Content)}, nil
}

func joinTextContent(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		} else {
			parts = append(parts, fmt.Sprint(c))
		}
	}
	return strings.Join(parts, "\n")
}

type headers struct {
	base   http.RoundTripper
	values map[string]string
}

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.values {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailBytes = 2048

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - tailBytes; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(t.buf), ""))
}

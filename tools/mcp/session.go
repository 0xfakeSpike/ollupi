package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Session struct {
	name    string
	title   string
	timeout time.Duration
	sess    *mcp.ClientSession
	tools   map[string]*mcp.Tool
	specs   []*tools.Spec
	skipped []string
	stderr  *tail

	mu   sync.Mutex
	down error
}

// connect reaches a server within ctx. One with a command runs in the box, for as long as running lasts.
func connect(ctx, running context.Context, client *mcp.Client, box sandbox.Box, s Server) (*Session, error) {
	out := &Session{name: s.Name, timeout: s.Timeout, tools: map[string]*mcp.Tool{}}
	if out.timeout <= 0 {
		out.timeout = defaultCallTimeout
	}
	var transport mcp.Transport
	if s.Command != "" {
		cwd := box.Roots()[0]
		program, err := box.Executable(ctx, s.Command, cwd)
		if err != nil {
			return nil, err
		}
		out.stderr = &tail{}
		process, err := box.Start(running, sandbox.Command{Program: program, Args: s.Args, Cwd: cwd, Env: s.Env}, out.stderr)
		if err != nil {
			return nil, err
		}
		defer func() {
			if out.sess == nil {
				process.Close()
			}
		}()
		transport = &mcp.IOTransport{Reader: process, Writer: process}
	} else {
		endpoint, err := url.Parse(s.URL)
		if err != nil {
			return nil, fmt.Errorf("mcp: url: %w", err)
		}
		httpClient := &http.Client{Transport: serverTransport{base: box.Transport(), host: endpoint.Host, headers: s.Headers}}
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
		if err == nil {
			err = errors.New("closed by the server")
		}
		out.down = out.withStderr(fmt.Errorf("disconnected: %w", err))
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

const metaCaller = "ollupi/caller"

type caller struct {
	ThreadID    string `json:"threadId"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Subagent    bool   `json:"subagent,omitempty"`
}

func (s *Session) Invoke(ctx context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	t, ok := s.tools[tool]
	if !ok {
		err := fmt.Errorf("mcp: tool %q not exposed by MCP server %q", tool, s.name)
		return tools.Result{}, err
	}
	params := &mcp.CallToolParams{Name: t.Name, Arguments: arguments}
	if c := tools.CallerOf(ctx); c.ThreadID != "" {
		params.Meta = mcp.Meta{metaCaller: caller{ThreadID: c.ThreadID, WorkspaceID: c.WorkspaceID, Subagent: c.Subagent}}
	}
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	res, err := s.sess.CallTool(call, params)
	if err != nil {
		if call.Err() != nil && ctx.Err() == nil {
			return tools.Result{}, fmt.Errorf("mcp: %q did not answer within %s; the call may still have taken effect, so check before repeating it", s.name, s.timeout)
		}
		return tools.Result{}, err
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

// joinTextContent is what the model reads of a result. Content that is not text is named, not dumped: its bytes
// would fill the model's context with digits.
func joinTextContent(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes, not shown]", c.MIMEType, len(c.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s, %d bytes, not shown]", c.MIMEType, len(c.Data)))
		case *mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource: %s %s]", c.Name, c.URI))
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, fmt.Sprintf("[resource: %s, %s, %d bytes, not shown]", c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob)))
			}
		default:
			parts = append(parts, fmt.Sprintf("[%T, not shown]", c))
		}
	}
	return strings.Join(parts, "\n")
}

// serverTransport sends a server's configured headers, its credentials among them, only to the server's own host,
// so a redirect elsewhere does not carry them.
type serverTransport struct {
	base    http.RoundTripper
	host    string
	headers map[string]string
}

func (t serverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := r.Context(), context.CancelFunc(func() {})
	if r.Method == http.MethodDelete {
		// The SDK ends a session with a DELETE that has no deadline: a server that does not answer would hold Close
		// forever.
		ctx, cancel = context.WithTimeout(ctx, closeTimeout)
	}
	r = r.Clone(ctx)
	if strings.EqualFold(r.URL.Host, t.host) {
		for k, v := range t.headers {
			r.Header.Set(k, v)
		}
	}
	res, err := t.base.RoundTrip(r)
	if err != nil {
		cancel()
		return nil, err
	}
	res.Body = cancelOnClose{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
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

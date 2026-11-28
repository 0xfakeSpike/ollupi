// Package cloudflare puts a Cloudflare sandbox bridge behind ollupi's sandbox
// interfaces: a deployment of this package is a Provider, each environment a
// sandbox the bridge creates, and the box it hands out an ollupi Box.
//
// The bridge is a small HTTP service (see the client this package began
// from): POST /v1/sandbox makes one, /exec streams a command's output as
// server-sent events, and /file/read and /file/write move bytes. What the
// bridge does not have, ollupi's Box asks of a shell inside the sandbox:
// stat, readdir and mkdir are `stat` and `find`, and the terminal is not
// there at all until the bridge grows a PTY endpoint.
package cloudflare

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// Config is one bridge a deployment offers.
type Config struct {
	// BaseURL is the bridge's address, e.g. https://sandbox.example.workers.dev.
	BaseURL string
	// APIKey is sent as a Bearer token.
	APIKey string
	// Name is what people call this provider's sandboxes. Empty is Cloudflare.
	Name string
	// Root is the folder of the sandbox that an environment's box is rooted
	// at. Empty is /workspace.
	Root string
}

// Provider is the bridge as a sandbox provider: one sandbox per environment.
type Provider struct {
	client *Client
	name   string
	root   string
}

func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("cloudflare: the bridge's URL is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("cloudflare: the bridge's token is required")
	}
	name := cfg.Name
	if name == "" {
		name = "Cloudflare"
	}
	root := cfg.Root
	if root == "" {
		root = "/workspace"
	}
	return &Provider{client: NewClient(cfg.BaseURL, cfg.APIKey), name: name, root: root}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Check(config json.RawMessage) error {
	if c := bytes.TrimSpace(config); len(c) > 0 && !bytes.Equal(c, []byte("{}")) && !bytes.Equal(c, []byte("null")) {
		return errors.New("cloudflare: this provider takes no settings")
	}
	return nil
}

// Open makes the environment's sandbox the first time and finds it again by
// the id it kept. A sandbox the bridge no longer has is made again.
func (p *Provider) Open(ctx context.Context, env sandbox.Env) (sandbox.Box, error) {
	kept := keptSandbox(env)
	m := &machine{client: p.client, id: kept.ID}
	if kept.ID == "" || !m.alive(ctx) {
		id, err := p.client.Create(ctx)
		if err != nil {
			return nil, err
		}
		kept.ID = id
		raw, err := json.Marshal(kept)
		if err != nil {
			return nil, err
		}
		if err := env.Keep(raw); err != nil {
			return nil, err
		}
		m.id = id
	}
	return &box{Box: sandbox.Remote(m, []string{p.root}, "Cloudflare sandbox"), m: m}, nil
}

// keptSandbox is the sandbox id an environment kept, if any.
func keptSandbox(env sandbox.Env) (state struct {
	ID string `json:"id"`
}) {
	if len(env.State) > 0 {
		_ = json.Unmarshal(env.State, &state)
	}
	return state
}

// box is the environment’s sandbox. The container is the boundary, so a
// command that asks for full access gets it; Remote refuses those by design,
// for a cloud that cannot lift its own limits.
type box struct {
	sandbox.Box
	m *machine
}

func (b *box) Run(ctx context.Context, c sandbox.Command) (sandbox.Result, error) {
	return b.m.Run(ctx, c)
}

func (b *box) Start(ctx context.Context, c sandbox.Command, stderr io.Writer) (sandbox.Process, error) {
	return b.m.Start(ctx, c, stderr)
}

func (b *box) CanEscalate() bool { return true }

// Folder keeps a folder that is no project of the environment inside
// /workspace, where the bridge's file route can read and write it; Remote
// would put it beside /workspace, at the container's root.
func (b *box) Folder(ctx context.Context, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("cloudflare: %q is not a folder name", name)
	}
	dir := path.Join("/workspace", ".ollupi", name)
	res, err := b.m.Run(ctx, sandbox.Command{Program: "mkdir", Args: []string{"-p", "--", dir}, Timeout: 15 * time.Second})
	if err != nil || res.ExitCode != 0 {
		return "", fmt.Errorf("cloudflare: make %s: %v %s", dir, err, strings.TrimSpace(res.Stderr))
	}
	return dir, nil
}

// Remove deletes the environment's sandbox. One that is gone already is no
// error.
func (p *Provider) Remove(ctx context.Context, env sandbox.Env) error {
	if kept := keptSandbox(env); kept.ID != "" {
		return p.client.Delete(ctx, kept.ID)
	}
	return nil
}

// machine is one sandbox as ollupi's Machine: commands, files, and nothing
// that needs a terminal.
type machine struct {
	client *Client
	id     string
}

func (m *machine) Run(ctx context.Context, c sandbox.Command) (sandbox.Result, error) {
	argv, timeout := commandArgv(c)
	var out, errOut strings.Builder
	exit := -1
	err := m.client.stream(ctx, m.id, argv, timeout, func(event string, data []byte, code int) error {
		switch event {
		case "stdout":
			out.Write(data)
		case "stderr":
			errOut.Write(data)
		case "exit":
			exit = code
		}
		return nil
	})
	if err != nil {
		return sandbox.Result{}, err
	}
	return sandbox.Result{Stdout: out.String(), Stderr: errOut.String(), ExitCode: exit}, nil
}

// Start streams a command: its output is Read, its close ends it. The bridge
// gives no way to write into a command, so Write fails.
func (m *machine) Start(ctx context.Context, c sandbox.Command, stderr io.Writer) (sandbox.Process, error) {
	argv, timeout := commandArgv(c)
	runCtx, cancel := context.WithCancel(ctx)
	body, err := m.client.execBody(runCtx, m.id, argv, timeout)
	if err != nil {
		cancel()
		return nil, err
	}
	out, write := io.Pipe()
	p := &process{out: out, cancel: func() { cancel(); body.Close() }}
	go func() {
		defer write.Close()
		_ = parseEvents(body, func(event string, data []byte, _ int) error {
			switch event {
			case "stdout":
				if _, err := write.Write(data); err != nil {
					return nil
				}
			case "stderr":
				if stderr != nil {
					_, _ = stderr.Write(data)
				}
			}
			return nil
		})
	}()
	return p, nil
}

// Stat asks the sandbox's stat(1): one line, tab separated.
func (m *machine) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	res, err := m.Run(ctx, sandbox.Command{Program: "stat", Args: []string{"-L", "-c", "%F\t%s\t%Y\t%a", "--", name}, Timeout: 15 * time.Second})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("stat %s: %s", name, strings.TrimSpace(res.Stderr))
	}
	fields := strings.Split(strings.TrimSpace(res.Stdout), "\t")
	if len(fields) != 4 {
		return nil, fmt.Errorf("stat %s: unexpected answer %q", name, res.Stdout)
	}
	size, _ := strconv.ParseInt(fields[1], 10, 64)
	mtime, _ := strconv.ParseInt(fields[2], 10, 64)
	mode, _ := strconv.ParseUint(fields[3], 8, 32)
	f := fileInfo{name: path.Base(name), size: size, mod: time.Unix(mtime, 0), mode: fs.FileMode(mode)}
	if strings.Contains(fields[0], "directory") {
		f.mode |= fs.ModeDir
	}
	if strings.Contains(fields[0], "symbolic link") {
		f.mode |= fs.ModeSymlink
	}
	return f, nil
}

// ReadDir asks the sandbox's find(1): one line per entry.
func (m *machine) ReadDir(ctx context.Context, dir string) ([]fs.DirEntry, error) {
	res, err := m.Run(ctx, sandbox.Command{Program: "find", Args: []string{dir, "-mindepth", "1", "-maxdepth", "1", "-printf", "%f\t%y\t%s\t%T@\n"}, Timeout: 15 * time.Second})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list %s: %s", dir, strings.TrimSpace(res.Stderr))
	}
	var entries []fs.DirEntry
	for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(fields[2], 10, 64)
		mtime, _ := strconv.ParseFloat(fields[3], 64)
		f := fileInfo{name: fields[0], size: size, mod: time.Unix(0, int64(mtime*1e9))}
		switch fields[1] {
		case "d":
			f.mode = fs.ModeDir | 0o755
		case "l":
			f.mode = fs.ModeSymlink | 0o777
		default:
			f.mode = 0o644
		}
		entries = append(entries, fs.FileInfoToDirEntry(f))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// Open streams a file's bytes from the bridge. Its file route takes a path
// inside /workspace, without the leading slash.
func (m *machine) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return m.client.openFile(ctx, m.id, name)
}

func (m *machine) WriteFile(ctx context.Context, name string, data []byte, perm fs.FileMode) error {
	if err := m.client.writeFile(ctx, m.id, name, data); err != nil {
		return err
	}
	if perm != 0 {
		if res, err := m.Run(ctx, sandbox.Command{Program: "chmod", Args: []string{fmt.Sprintf("%o", perm.Perm()), name}, Timeout: 15 * time.Second}); err != nil || res.ExitCode != 0 {
			return fmt.Errorf("chmod %s: %v %s", name, err, strings.TrimSpace(res.Stderr))
		}
	}
	return nil
}

// Terminal opens the bridge's PTY. A shell that starts in a folder or with
// environment variables needs a session there, so one is made first.
func (m *machine) Terminal(ctx context.Context, spec sandbox.TerminalSpec) (sandbox.Terminal, error) {
	session := ""
	if spec.Cwd != "" || len(spec.Env) > 0 {
		id, err := m.client.createSession(ctx, m.id, spec.Cwd, spec.Env)
		if err != nil {
			return nil, err
		}
		session = id
	}
	return m.client.pty(ctx, m.id, spec, session)
}

// Close lets the sandbox be: the environment keeps it for the next Open.
func (m *machine) Close() error { return nil }

func (m *machine) alive(ctx context.Context) bool {
	running, err := m.client.running(ctx, m.id)
	return err == nil && running
}

// commandArgv is the command as the bridge takes it, with the folder and the
// environment a command names put around it by a shell.
func commandArgv(c sandbox.Command) ([]string, int) {
	timeout := int(c.Timeout / time.Millisecond)
	if timeout <= 0 {
		timeout = 30_000
	}
	if c.Cwd == "" && len(c.Env) == 0 {
		return append([]string{c.Program}, c.Args...), timeout
	}
	var script strings.Builder
	if c.Cwd != "" {
		script.WriteString("cd " + shellQuote(c.Cwd) + " && ")
	}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		script.WriteString(k + "=" + shellQuote(c.Env[k]) + " ")
	}
	script.WriteString("exec ")
	for i, arg := range append([]string{c.Program}, c.Args...) {
		if i > 0 {
			script.WriteByte(' ')
		}
		script.WriteString(shellQuote(arg))
	}
	return []string{"sh", "-lc", script.String()}, timeout
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fileInfo is what stat and find tell of an entry.
type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
	mod  time.Time
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return f.mode }
func (f fileInfo) ModTime() time.Time { return f.mod }
func (f fileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fileInfo) Sys() any           { return nil }

// process is a command a Start streams: what it prints is read, and close
// ends it. The bridge takes no input, so Write says so.
type process struct {
	out    *io.PipeReader
	cancel func()
	once   bool
}

func (p *process) Read(b []byte) (int, error) { return p.out.Read(b) }
func (p *process) Write([]byte) (int, error) {
	return 0, errors.New("cloudflare: this sandbox takes no input")
}
func (p *process) Close() error {
	if !p.once {
		p.once = true
		p.cancel()
		_ = p.out.Close()
	}
	return nil
}

// Client is the bridge's HTTP API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: &http.Client{}}
}

func NewClientFromEnv() (*Client, error) {
	u, k := os.Getenv("SANDBOX_API_URL"), os.Getenv("SANDBOX_API_KEY")
	if u == "" || k == "" {
		return nil, errors.New("cloudflare: SANDBOX_API_URL and SANDBOX_API_KEY must both be set")
	}
	return NewClient(u, k), nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func readErr(resp *http.Response, op string) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("cloudflare %s: %s: %s", op, resp.Status, strings.TrimSpace(string(b)))
}

func (c *Client) Health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return readErr(resp, "health")
	}
	resp.Body.Close()
	return nil
}

// Create makes a sandbox and answers its id.
func (c *Client) Create(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/sandbox", nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", readErr(resp, "create sandbox")
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("cloudflare create sandbox: no id")
	}
	return out.ID, nil
}

// Delete removes a sandbox. One the bridge does not have is not an error.
func (c *Client) Delete(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/sandbox/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return readErr(resp, "delete sandbox")
	}
	resp.Body.Close()
	return nil
}

// execBody asks for a command's events and answers the body, which is read
// as server-sent events.
func (c *Client) execBody(ctx context.Context, id string, argv []string, timeoutMs int) (io.ReadCloser, error) {
	body, _ := json.Marshal(map[string]any{"argv": argv, "timeout_ms": timeoutMs})
	resp, err := c.do(ctx, http.MethodPost, "/v1/sandbox/"+url.PathEscape(id)+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, readErr(resp, "exec")
	}
	return resp.Body, nil
}

// stream runs a command and gives each event, till the command ends.
func (c *Client) stream(ctx context.Context, id string, argv []string, timeoutMs int, onEvent func(event string, data []byte, exit int) error) error {
	body, err := c.execBody(ctx, id, argv, timeoutMs)
	if err != nil {
		return err
	}
	defer body.Close()
	return parseEvents(body, onEvent)
}

// parseEvents reads the bridge's server-sent events: stdout and stderr are
// base64 of the bytes, exit is a JSON object with the code, and error is a
// failure of the command itself. Some bridges wrap output as
// {"data":"<base64>"}; both are read.
func parseEvents(r io.Reader, onEvent func(event string, data []byte, exit int) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			switch event {
			case "error":
				var e struct {
					Error string `json:"error"`
					Code  string `json:"code"`
				}
				_ = json.Unmarshal([]byte(raw), &e)
				return fmt.Errorf("cloudflare: %s (%s)", e.Error, e.Code)
			case "exit":
				var p struct {
					ExitCode int `json:"exit_code"`
				}
				_ = json.Unmarshal([]byte(raw), &p)
				if err := onEvent("exit", nil, p.ExitCode); err != nil {
					return err
				}
			default:
				data, err := base64.StdEncoding.DecodeString(raw)
				if err != nil {
					var p struct {
						Data string `json:"data"`
					}
					if json.Unmarshal([]byte(raw), &p) != nil {
						continue
					}
					data, err = base64.StdEncoding.DecodeString(p.Data)
					if err != nil {
						continue
					}
				}
				if err := onEvent(event, data, 0); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

// filePath is a path as the bridge's file route takes it: inside /workspace,
// without the leading slash. Anything outside is refused here rather than by
// a 403 later.
func filePath(name string) (string, error) {
	clean := path.Clean(name)
	if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
		return "", fmt.Errorf("cloudflare: %s is outside the sandbox's /workspace", name)
	}
	return strings.TrimPrefix(clean, "/"), nil
}

func escapeFilePath(name string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c *Client) openFile(ctx context.Context, id, name string) (io.ReadCloser, error) {
	p, err := filePath(name)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, "/v1/sandbox/"+url.PathEscape(id)+"/file/"+escapeFilePath(p), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, readErr(resp, "read file")
	}
	return resp.Body, nil
}

func (c *Client) writeFile(ctx context.Context, id, name string, data []byte) error {
	p, err := filePath(name)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/v1/sandbox/"+url.PathEscape(id)+"/file/"+escapeFilePath(p), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return readErr(resp, "write file")
	}
	resp.Body.Close()
	return nil
}

// running asks whether the sandbox's container is alive.
func (c *Client) running(ctx context.Context, id string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/sandbox/"+url.PathEscape(id)+"/running", nil)
	if err != nil {
		return false, err
	}
	if resp.StatusCode/100 != 2 {
		return false, readErr(resp, "running")
	}
	defer resp.Body.Close()
	var out struct {
		Running bool `json:"running"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Running, nil
}

// createSession makes a session whose folder and environment the terminal
// starts with; its id scopes the PTY.
func (c *Client) createSession(ctx context.Context, id, cwd string, env map[string]string) (string, error) {
	body := map[string]any{}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if len(env) > 0 {
		body["env"] = env
	}
	raw, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPost, "/v1/sandbox/"+url.PathEscape(id)+"/session", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", readErr(resp, "session")
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("cloudflare session: no id")
	}
	return out.ID, nil
}

// pty opens the bridge's terminal: binary frames are the shell's bytes, text
// frames are its status and our resize.
func (c *Client) pty(ctx context.Context, id string, spec sandbox.TerminalSpec, session string) (sandbox.Terminal, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("cloudflare terminal: %s is not an http address", c.baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/sandbox/" + url.PathEscape(id) + "/pty"
	q := u.Query()
	if spec.Cols > 0 {
		q.Set("cols", strconv.Itoa(spec.Cols))
	}
	if spec.Rows > 0 {
		q.Set("rows", strconv.Itoa(spec.Rows))
	}
	if session != "" {
		q.Set("session", session)
	}
	u.RawQuery = q.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": {"Bearer " + c.apiKey}}})
	if err != nil {
		return nil, fmt.Errorf("cloudflare terminal: %w", err)
	}
	return newTerminal(conn), nil
}

// terminal is a PTY over the bridge's WebSocket: Read is what the shell
// prints, Write what the person types, Resize its window, Wait its exit.
type terminal struct {
	conn   *websocket.Conn
	out    *io.PipeReader
	write  *io.PipeWriter
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	code   int
	err    error
}

func newTerminal(conn *websocket.Conn) *terminal {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	t := &terminal{conn: conn, out: pr, write: pw, cancel: cancel, done: make(chan struct{}), code: -1}
	go t.read(ctx)
	return t
}

func (t *terminal) read(ctx context.Context) {
	defer t.finish()
	for {
		typ, data, err := t.conn.Read(ctx)
		if err != nil {
			t.fail(err)
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := t.write.Write(data); err != nil {
				return
			}
			continue
		}
		var m struct {
			Type    string `json:"type"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "exit":
			t.mu.Lock()
			t.code = m.Code
			t.mu.Unlock()
			return
		case "error":
			t.fail(errors.New(m.Message))
			return
		}
	}
}

func (t *terminal) fail(err error) {
	t.mu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.mu.Unlock()
}

func (t *terminal) finish() {
	t.once.Do(func() {
		_ = t.write.Close()
		close(t.done)
	})
}

func (t *terminal) Read(b []byte) (int, error) { return t.out.Read(b) }

func (t *terminal) Write(b []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.conn.Write(ctx, websocket.MessageBinary, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (t *terminal) Resize(cols, rows int) error {
	msg, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return t.conn.Write(ctx, websocket.MessageText, msg)
}

func (t *terminal) Wait() (int, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.code, t.err
}

func (t *terminal) Close() error {
	select {
	case <-t.done:
		return nil
	default:
	}
	t.cancel()
	_ = t.conn.Close(websocket.StatusNormalClosure, "closed")
	t.fail(errors.New("terminal closed"))
	t.finish()
	return nil
}

package mcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server is one configured MCP server: a command run over stdio, or a
// streamable HTTP endpoint. Name identifies it everywhere: it is the
// provider id, the prefix of its tool names and the name in its status.
type Server struct {
	Name string

	Command string
	Args    []string
	Env     map[string]string

	URL     string
	Headers map[string]string
}

type Client struct {
	Name    string
	Version string
}

// ServerStatus is how a configured server is doing. Env and header values
// never appear in it.
type ServerStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools"`
	// SkippedTools are the server's tools whose prefixed names the model
	// API would refuse, so they are not offered.
	SkippedTools []string `json:"skippedTools,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Servers are the configured servers after connecting. A server that is
// misconfigured or cannot be reached is left out, with its reason in its
// status, so the rest of the app starts without it.
type Servers struct {
	servers  []Server
	sessions []*Session
	failures []error
}

var (
	serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	toolName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

const connectTimeout = 30 * time.Second

// Connect connects to every server at once and returns when each has
// connected and listed its tools, or failed.
func Connect(ctx context.Context, client Client, servers []Server) *Servers {
	if client.Name == "" {
		client.Name = "ollupi"
	}
	if client.Version == "" {
		client.Version = "0.1"
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: client.Name, Version: client.Version}, nil)
	out := &Servers{servers: servers, sessions: make([]*Session, len(servers)), failures: make([]error, len(servers))}
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i, s := range servers {
		if err := check(s, seen); err != nil {
			out.failures[i] = err
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			out.sessions[i], out.failures[i] = connect(ctx, mc, s)
		}()
	}
	wg.Wait()
	return out
}

func check(s Server, seen map[string]bool) error {
	switch {
	case !serverName.MatchString(s.Name) || strings.Contains(s.Name, separator):
		return fmt.Errorf("the name %q must be 1 to 32 letters, digits, '-' or '_', start with a letter or digit, and not contain %q", s.Name, separator)
	case seen[s.Name]:
		return fmt.Errorf("another server is already named %q", s.Name)
	case (s.Command == "") == (s.URL == ""):
		return errors.New("give either a command (stdio) or a URL (HTTP)")
	case s.Command == "" && (len(s.Args) > 0 || len(s.Env) > 0):
		return errors.New("args and env are for a command; this server has a URL")
	case s.URL == "" && len(s.Headers) > 0:
		return errors.New("headers are for a URL; this server has a command")
	}
	seen[s.Name] = true
	return nil
}

// Providers are the connected servers, to register as tool providers.
func (s *Servers) Providers() []tools.Provider {
	var out []tools.Provider
	for _, sess := range s.sessions {
		if sess != nil {
			out = append(out, sess)
		}
	}
	return out
}

// Statuses reports every configured server in configuration order; a
// server that has disconnected since reports why.
func (s *Servers) Statuses() []ServerStatus {
	out := make([]ServerStatus, len(s.servers))
	for i, server := range s.servers {
		st := ServerStatus{Name: server.Name}
		err := s.failures[i]
		if sess := s.sessions[i]; sess != nil {
			st.Tools, st.SkippedTools = len(sess.specs), sess.skipped
			sess.mu.Lock()
			err = sess.down
			sess.mu.Unlock()
		}
		st.Connected = err == nil
		if err != nil {
			st.Error = err.Error()
		}
		out[i] = st
	}
	return out
}

func (s *Servers) Close() error {
	var errs []error
	for _, sess := range s.sessions {
		if sess != nil {
			errs = append(errs, sess.sess.Close())
		}
	}
	return errors.Join(errs...)
}

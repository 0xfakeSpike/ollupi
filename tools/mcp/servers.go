package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

type ServerStatus struct {
	Name         string   `json:"name"`
	Connected    bool     `json:"connected"`
	Tools        int      `json:"tools"`
	SkippedTools []string `json:"skippedTools,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type Servers struct {
	servers  []Server
	named    []bool
	sessions []*Session
	failures []error
	set      *tools.Set
}

var (
	serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	toolName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

const connectTimeout = 30 * time.Second

// Users keeps each user's servers. They start the first time the user needs them, in the user's own sandbox, and start
// again once one has gone, as it does when the sandbox sleeps.
type Users struct {
	ctx    context.Context
	client *mcp.Client
	config func(user string) ([]Server, error)
	boxFor func(user string) (sandbox.Box, error)

	mu    sync.Mutex
	users map[string]*connected
}

// connected holds a user's servers, and makes their other callers wait while they connect.
type connected struct {
	mu      sync.Mutex
	servers *Servers
}

// NewUsers takes what each user has configured, credentials included, and where their commands run. Their processes
// last until ctx ends.
func NewUsers(ctx context.Context, client Client, config func(user string) ([]Server, error), boxFor func(user string) (sandbox.Box, error)) *Users {
	if client.Name == "" {
		client.Name = "ollupi"
	}
	if client.Version == "" {
		client.Version = "0.1"
	}
	return &Users{ctx: ctx, client: mcp.NewClient(&mcp.Implementation{Name: client.Name, Version: client.Version}, nil),
		config: config, boxFor: boxFor, users: map[string]*connected{}}
}

// For gives the user's servers, connecting them if they are not.
func (u *Users) For(user string) (*Servers, error) {
	u.mu.Lock()
	c := u.users[user]
	if c == nil {
		c = &connected{}
		u.users[user] = c
	}
	u.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.servers != nil && !c.servers.gone() {
		return c.servers, nil
	}
	if c.servers != nil {
		if err := c.servers.Close(); err != nil {
			slog.Warn("mcp servers not closed", "user", user, "err", err)
		}
		c.servers = nil
	}
	servers, err := u.config(user)
	if err != nil {
		return nil, fmt.Errorf("mcp: the servers of the user: %w", err)
	}
	box, err := u.boxFor(user)
	if err != nil {
		return nil, fmt.Errorf("mcp: the sandbox of the user: %w", err)
	}
	c.servers = u.connect(box, servers)
	return c.servers, nil
}

// Tools gives the user's tools, the same Set for as long as the same servers run.
func (u *Users) Tools(user string) (*tools.Set, error) {
	s, err := u.For(user)
	if err != nil {
		return nil, err
	}
	return s.set, nil
}

// Close stops every user's servers.
func (u *Users) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	var errs []error
	for _, c := range u.users {
		c.mu.Lock()
		if c.servers != nil {
			errs = append(errs, c.servers.Close())
		}
		c.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (u *Users) connect(box sandbox.Box, servers []Server) *Servers {
	out := &Servers{servers: servers, named: make([]bool, len(servers)), sessions: make([]*Session, len(servers)), failures: make([]error, len(servers))}
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i, s := range servers {
		if err := checkName(s.Name, seen); err != nil {
			out.failures[i] = err
			continue
		}
		seen[s.Name], out.named[i] = true, true
		if err := checkTransport(s); err != nil {
			out.failures[i] = err
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(u.ctx, connectTimeout)
			defer cancel()
			out.sessions[i], out.failures[i] = connect(ctx, u.ctx, u.client, box, s)
		}()
	}
	wg.Wait()
	out.set = &tools.Set{}
	for i, sess := range out.sessions {
		switch {
		case sess != nil:
			out.set.Providers = append(out.set.Providers, sess)
		case out.named[i]:
			out.set.Unavailable = append(out.set.Unavailable, servers[i].Name)
		}
	}
	return out
}

func checkName(name string, seen map[string]bool) error {
	switch {
	case !serverName.MatchString(name) || strings.Contains(name, tools.ProviderSeparator):
		return fmt.Errorf("the name %q must be 1 to 32 letters, digits, '-' or '_', start with a letter or digit, and not contain %q", name, tools.ProviderSeparator)
	case seen[name]:
		return fmt.Errorf("another server is already named %q", name)
	}
	return nil
}

func checkTransport(s Server) error {
	switch {
	case (s.Command == "") == (s.URL == ""):
		return errors.New("give either a command (stdio) or a URL (HTTP)")
	case s.Command == "" && (len(s.Args) > 0 || len(s.Env) > 0):
		return errors.New("args and env are for a command; this server has a URL")
	case s.URL == "" && len(s.Headers) > 0:
		return errors.New("headers are for a URL; this server has a command")
	}
	return nil
}

// gone says a server that was connected has since dropped.
func (s *Servers) gone() bool {
	for _, sess := range s.sessions {
		if sess != nil {
			sess.mu.Lock()
			down := sess.down
			sess.mu.Unlock()
			if down != nil {
				return true
			}
		}
	}
	return false
}

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

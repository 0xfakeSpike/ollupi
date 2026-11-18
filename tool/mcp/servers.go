package mcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	user.MCPServer
	Timeout time.Duration
}

type Client struct {
	Name    string
	Version string
}

var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	connectTimeout     = 30 * time.Second
	defaultCallTimeout = 5 * time.Minute
	closeTimeout       = 5 * time.Second
)

type Users struct {
	ctx      context.Context
	client   *mcp.Client
	st       store.Store
	boxes    func(user string) (sandbox.Box, error)
	builtins []kit.Provider
	sub      *store.Subscription

	mu    sync.Mutex
	users map[string]*connected
}

type connected struct {
	mu       sync.Mutex
	servers  []Server
	sessions []*Session
	failures []error
	set      tool.Set
}

func NewUsers(ctx context.Context, client Client, st store.Store, boxes func(user string) (sandbox.Box, error), builtins ...kit.Provider) *Users {
	u := &Users{ctx: ctx, client: mcp.NewClient(&mcp.Implementation{Name: client.Name, Version: client.Version}, nil),
		st: st, boxes: boxes, builtins: builtins, users: map[string]*connected{}}
	u.sub = st.Feed().SubscribeAll(u.hear)
	return u
}

func (u *Users) hear(e store.Event) {
	changed := e.Owned != nil && (e.Owned.MCPServer != nil || e.Owned.MCPServerDeleted != "")
	if !changed {
		return
	}
	u.mu.Lock()
	c := u.users[e.User]
	delete(u.users, e.User)
	u.mu.Unlock()
	if c != nil {
		go c.close()
	}
}

func (u *Users) of(name string) (*connected, error) {
	u.mu.Lock()
	c := u.users[name]
	if c == nil {
		c = &connected{}
		u.users[name] = c
	}
	u.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set != nil && !c.gone() {
		return c, nil
	}
	c.closeLocked()
	if err := u.connect(c, name); err != nil {
		return nil, err
	}
	return c, nil
}

func (u *Users) Tools(ctx context.Context, name string) (tool.Set, error) {
	c, err := u.of(name)
	if err != nil {
		return nil, err
	}
	return c.set, nil
}

func (u *Users) Statuses(name string) ([]user.MCPStatus, error) {
	c, err := u.of(name)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]user.MCPStatus, len(c.servers))
	for i, server := range c.servers {
		st := user.MCPStatus{Name: server.Name}
		err := c.failures[i]
		if sess := c.sessions[i]; sess != nil {
			st.Tools, st.Skipped = len(sess.specs), sess.skipped
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
	return out, nil
}

func (u *Users) Close() error {
	u.sub.Close()
	u.mu.Lock()
	defer u.mu.Unlock()
	var errs []error
	for _, c := range u.users {
		errs = append(errs, c.close())
	}
	return errors.Join(errs...)
}

func (u *Users) connect(c *connected, name string) error {
	var stored []user.MCPServer
	if err := u.st.Read(u.ctx, func(r store.Reader) (err error) { stored, err = r.MCPServers(name); return err }); err != nil {
		return fmt.Errorf("mcp: the servers of the user: %w", err)
	}
	box, err := u.boxes(name)
	if err != nil {
		return fmt.Errorf("mcp: the sandbox of the user: %w", err)
	}
	c.servers = make([]Server, len(stored))
	c.sessions, c.failures = make([]*Session, len(stored)), make([]error, len(stored))
	var wg sync.WaitGroup
	for i, s := range stored {
		c.servers[i] = Server{MCPServer: s}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(u.ctx, connectTimeout)
			defer cancel()
			c.sessions[i], c.failures[i] = connect(ctx, u.ctx, u.client, box, c.servers[i])
		})
	}
	wg.Wait()
	providers := append([]kit.Provider{}, u.builtins...)
	var unavailable []string
	for i, sess := range c.sessions {
		if sess != nil {
			providers = append(providers, sess)
		} else {
			unavailable = append(unavailable, c.servers[i].Name)
		}
	}
	set, err := kit.New(u.ctx, unavailable, providers...)
	if err != nil {
		c.closeLocked()
		return err
	}
	c.set = set
	return nil
}

func (c *connected) gone() bool {
	for _, sess := range c.sessions {
		if sess == nil {
			continue
		}
		sess.mu.Lock()
		down := sess.down
		sess.mu.Unlock()
		if down != nil {
			return true
		}
	}
	return false
}

func (c *connected) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *connected) closeLocked() error {
	var errs []error
	for _, sess := range c.sessions {
		if sess != nil {
			errs = append(errs, sess.sess.Close())
		}
	}
	c.servers, c.sessions, c.failures, c.set = nil, nil, nil, nil
	return errors.Join(errs...)
}

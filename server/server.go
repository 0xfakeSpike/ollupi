// Package server puts one ollupi server together: ollupi's own host (the
// store, the models, the flows that run turns, learn memory and send
// scheduled tasks, the app and its gateway, its tools) with this machine as
// one of the sandboxes conversations can run in, the profile it starts with,
// and Serve to run it. The desktop's backend and a standalone `ollupi serve`
// differ only in what they pass here: the browser's tools, the skills a new
// profile starts with, where the first profile's scratch folder is, and what
// the first profile is called.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/auth"
	"github.com/0xfakeSpike/ollupi/gateway"
	ollupi "github.com/0xfakeSpike/ollupi/host"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"golang.org/x/sync/errgroup"
)

// Profile is the profile a new store starts with.
type Profile struct {
	// ID names it: the part of every token after its dot.
	ID string
	// Name is what people see it by; empty leaves it unnamed.
	Name string
}

// Config is what a server is built from.
type Config struct {
	// Home is the server's folder: each environment of this machine is a
	// folder of users/<profile>/ in it.
	Home string
	// DataHome holds the store and the attachments; empty means Home.
	DataHome string
	// Name is what the server calls itself, to the agent and to MCP servers.
	Name string
	// Token is the gateway's secret: a connection's token is it, a dot and
	// the profile the connection acts as.
	Token string
	// OperatorToken is the operator's secret, given the same way: a
	// connection with it acts as the profile and as the deployment's
	// operator.
	OperatorToken string
	// AllowedOrigins are WebSocket origin patterns beyond loopback, e.g. the
	// packaged renderer's app://ollupi.
	AllowedOrigins []string
	// AllowedHosts are Host header names beyond localhost, e.g. behind a
	// reverse proxy that keeps the public name.
	AllowedHosts []string
	// Tools are the deployment's own tool providers, as the app's browser.
	Tools []kit.Provider
	// Sandboxes are the other providers a profile's environments can be
	// of, by id (a cloud's); this machine is always one, as LocalSandbox.
	// Each environment is a sandbox of exactly one, and a profile may have
	// environments of several: switching environment switches the sandbox.
	Sandboxes map[string]sandbox.Provider
	// Sandbox is the provider a new profile's first environment is of;
	// empty is LocalSandbox.
	Sandbox string
	// First is the profile a store without one starts with.
	First Profile
	// StarterSkills, when set, gives every new profile its first skills.
	StarterSkills func() ([]skill.Package, error)
	// Scratch, when set, is the folder under Home that the first profile's
	// first environment uses, instead of users/<profile>/<environment>.
	Scratch string
}

// Server is a built backend and the gateway that serves it.
type Server struct {
	*ollupi.Host
	authenticate auth.Authenticate
}

// New builds the server within ctx, which the users' MCP servers run in. It
// does not listen or run anything.
func New(ctx context.Context, cfg Config) (_ *Server, err error) {
	if strings.TrimSpace(cfg.First.ID) == "" {
		return nil, errors.New("server: the first profile's id is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = Name
	}
	dataHome := cfg.DataHome
	if dataHome == "" {
		dataHome = cfg.Home
	}

	// The store is the host's, made below; a call reaches these only once it
	// is.
	var st store.Store
	var users *app.App
	authenticate := auth.Profiles(cfg.Token, cfg.OperatorToken, func(ctx context.Context) []string { return Profiles(ctx, st) })
	var welcome func(context.Context, string) error
	if cfg.StarterSkills != nil {
		welcome = func(ctx context.Context, user string) error {
			return giveStarterSkills(ctx, users, cfg.StarterSkills, user)
		}
	}

	// This machine is always one of the sandboxes: every environment of it is
	// a folder of the home, commands in it limited as ollupi limits them.
	sandboxID := cfg.Sandbox
	if sandboxID == "" {
		sandboxID = LocalSandbox
	}
	sandboxes := make(map[string]sandbox.Provider, len(cfg.Sandboxes)+1)
	for id, p := range cfg.Sandboxes {
		if p == nil {
			return nil, fmt.Errorf("server: sandbox %q has no provider", id)
		}
		sandboxes[id] = p
	}
	sandboxes[LocalSandbox] = thisMachine(cfg.Home, dataHome, cfg.First.ID, cfg.Scratch, func(ctx context.Context, user string) (string, error) {
		return firstEnvironment(ctx, st, user)
	})
	if _, ok := sandboxes[sandboxID]; !ok {
		return nil, fmt.Errorf("server: the first sandbox %q is not one this server offers", sandboxID)
	}

	h, err := ollupi.New(ctx, ollupi.Config{
		Home:  dataHome,
		Name:  cfg.Name,
		Tools: cfg.Tools,
		// The app's Super Agent is ollupi's hub: the user's one hub
		// workspace hands work out to the project workspaces.
		Hub:       true,
		Sandboxes: sandboxes,
		Sandbox:   sandboxID,
		Gateway: &gateway.Options{
			Authenticate: authenticate,
			// A user the operator makes starts with the deployment's skills.
			Welcome:        welcome,
			AllowedOrigins: cfg.AllowedOrigins,
			AllowedHosts:   cfg.AllowedHosts,
		},
	})
	if err != nil {
		return nil, err
	}
	st, users = h.Store, h.App
	s := &Server{Host: h, authenticate: authenticate}
	if err := s.firstProfile(ctx, cfg); err != nil {
		h.Close()
		return nil, err
	}
	return s, nil
}

// Authenticate names the identity a connection's token acts as. It does not
// make profiles: a token names one the store holds.
func (s *Server) Authenticate(ctx context.Context, token string) (auth.Identity, error) {
	if s.Host == nil {
		return auth.Identity{}, auth.ErrUnknownToken
	}
	return s.authenticate(ctx, token)
}

// Profiles are the users of the store, by id.
func (s *Server) Profiles(ctx context.Context) []string { return Profiles(ctx, s.Host.Store) }

// Serve serves the gateway on ln and runs ollupi's flows until ctx ends or
// one of them fails, then releases the store.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	defer func() {
		if err := s.Host.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "server: not closed:", err)
		}
	}()
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return s.Host.Run(ctx) })
	g.Go(func() error { return s.Host.Gateway.Serve(ctx, ln) })
	// Each profile's MCP servers connect and list their tools now rather
	// than in the first turn; one that cannot is skipped, and mcpServer/list
	// says why.
	for _, user := range Profiles(ctx, s.Host.Store) {
		g.Go(func() error {
			s.Host.Users.Tools(ctx, user)
			return nil
		})
	}
	return g.Wait()
}

// firstProfile makes a store without users its first, as cfg says.
func (s *Server) firstProfile(ctx context.Context, cfg Config) error {
	if len(Profiles(ctx, s.Host.Store)) > 0 {
		return nil
	}
	if err := s.Host.App.EnsureUser(ctx, cfg.First.ID); err != nil {
		return err
	}
	if cfg.First.Name != "" {
		if _, err := s.Host.App.RenameUser(ctx, cfg.First.ID, cfg.First.Name); err != nil {
			return err
		}
	}
	if cfg.StarterSkills != nil {
		return giveStarterSkills(ctx, s.Host.App, cfg.StarterSkills, cfg.First.ID)
	}
	return nil
}

// giveStarterSkills gives a profile the skills the deployment starts it with.
func giveStarterSkills(ctx context.Context, users *app.App, starter func() ([]skill.Package, error), profile string) error {
	pkgs, err := starter()
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		if _, err := users.SetSkill(ctx, profile, p); err != nil {
			return err
		}
	}
	return nil
}

// Name is what a server calls itself, to the agent and to MCP servers.
const Name = "ollupi"

// LocalSandbox is what an environment of this machine names its provider.
const LocalSandbox = "local"

// FirstEnvironment is the environment a profile is made with: this machine.
func FirstEnvironment() account.Environment {
	return account.NewEnvironment(sandbox.LocalName, LocalSandbox, nil)
}

// thisMachine is this machine as a provider: its environments are folders
// under home, and commands in them run under Seatbelt. When scratch is set,
// the first profile's first environment keeps that folder of the home, as its
// Super Agent does; every other one has a folder of its own,
// users/<profile>/<environment>, so two never write the same files. first
// names a profile's first environment. Commands may not read the store, which
// holds the models' API keys and every profile's MCP servers; one with full
// access gets the backend's environment but the OLLUPI_ variables, which hold
// its tokens.
func thisMachine(home, dataHome, firstProfile, scratch string, first func(ctx context.Context, user string) (string, error)) sandbox.Local {
	deny := []string{filepath.Join(dataHome, "data")}
	if home != dataHome {
		deny = append(deny, filepath.Join(home, "data"))
	}
	var folder func(context.Context, sandbox.Env) (string, error)
	if scratch != "" {
		folder = func(ctx context.Context, env sandbox.Env) (string, error) {
			if env.User != firstProfile {
				return "", nil
			}
			id, err := first(ctx, env.User)
			if err != nil || id != env.ID {
				return "", err
			}
			return filepath.Join(home, scratch), nil
		}
	}
	return sandbox.Local{
		Dir:    filepath.Join(home, "users"),
		Folder: folder,
		Config: sandbox.Config{
			DenyRead:  deny,
			EnvMarker: "OLLUPI_SANDBOX",
			HideEnv:   []string{"OLLUPI_"},
		},
	}
}

// firstEnvironment is the id of a profile's first environment, the one it
// was made with.
func firstEnvironment(ctx context.Context, st store.Store, user string) (id string, err error) {
	if st == nil {
		return "", nil
	}
	err = st.Read(ctx, func(r store.Reader) error {
		envs, err := r.Environments(user)
		if err == nil && len(envs) > 0 {
			id = envs[0].ID
		}
		return err
	})
	return id, err
}

// Profiles lists the users the store holds.
func Profiles(ctx context.Context, st store.Store) []string {
	if st == nil {
		return nil
	}
	var users []string
	_ = st.Read(ctx, func(r store.Reader) error {
		var err error
		users, err = r.Users()
		return err
	})
	return users
}

// Package resources keeps what turns run on: the models and the connections that serve them, each model's context
// window, and the tools of each user.
package resources

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Config is what the resources are made of.
type Config struct {
	// Models are the models turns may run on, one per connection; the first is the default.
	Models      []llm.Model
	Connections map[string]llm.Client
	// ClientFor gives the client a user's turns call a connection through, so that they run on the user's own key
	// and quota; it should return the same client each time. Without it users share Connections.
	ClientFor func(user, connection string) (llm.Client, error)
	// ToolsFor adds the tools one user has, such as their MCP servers.
	ToolsFor func(user string) (*tools.Set, error)
}

// Resources are the models and tools of a shard.
type Resources struct {
	cfg Config
	ctx context.Context
	// providers are what every user's registry starts from, and registry what they give.
	providers  []tools.Provider
	registry   *tools.Registry
	registries sync.Map
}

// userRegistry is a user's registry and the Set it was built from.
type userRegistry struct {
	set      *tools.Set
	registry *tools.Registry
}

// New checks the models and makes the tools every user has from providers. A user's own tools connect within ctx.
func New(ctx context.Context, cfg Config, providers []tools.Provider) (*Resources, error) {
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("resources: at least one model is required")
	}
	for i, m := range cfg.Models {
		if m.Connection == "" || strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("resources: every model needs a connection and a name")
		}
		if slices.ContainsFunc(cfg.Models[:i], func(o llm.Model) bool { return o.Connection == m.Connection }) {
			return nil, fmt.Errorf("resources: connection %q is listed twice", m.Connection)
		}
		if cfg.Connections[m.Connection] == nil {
			return nil, fmt.Errorf("resources: connection %q has no client", m.Connection)
		}
	}
	registry, err := tools.New(ctx, nil, providers...)
	if err != nil {
		return nil, fmt.Errorf("resources: %w", err)
	}
	return &Resources{cfg: cfg, ctx: ctx, providers: providers, registry: registry}, nil
}

// Models are the models turns may run on, with the connection of the default.
func (r *Resources) Models() ([]llm.Model, string) {
	models := make([]llm.Model, 0, len(r.cfg.Models))
	for _, m := range r.cfg.Models {
		models = append(models, m.Clone())
	}
	return models, r.cfg.Models[0].Connection
}

// Model is the model on the connection.
func (r *Resources) Model(connection string) (llm.Model, error) {
	i := slices.IndexFunc(r.cfg.Models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", connection)
	}
	return r.cfg.Models[i].Clone(), nil
}

// Window is the context window of the model.
func (r *Resources) Window(m llm.Model) (*contextmanager.Manager, error) {
	limits, err := r.cfg.Connections[m.Connection].Limits(m.Name)
	if err != nil {
		return nil, err
	}
	return contextmanager.New(m.Name, limits), nil
}

// Client is the client the user's turns call the connection through.
func (r *Resources) Client(user, connection string) (llm.Client, error) {
	if r.cfg.ClientFor != nil {
		return r.cfg.ClientFor(user, connection)
	}
	return r.cfg.Connections[connection], nil
}

// Shared says users have tools of their own besides the ones they share, so that settings shared by all may name
// tools one user lacks.
func (r *Resources) Shared() bool { return r.cfg.ToolsFor != nil }

// Tools are the tools every user has.
func (r *Resources) Tools() *tools.Registry { return r.registry }

// Registry gives the tools the user has. The first call may wait for their servers to connect.
func (r *Resources) Registry(user string) (*tools.Registry, error) {
	if r.cfg.ToolsFor == nil {
		return r.registry, nil
	}
	set, err := r.cfg.ToolsFor(user)
	if err != nil {
		return nil, fmt.Errorf("resources: the tools of the user: %w", err)
	}
	if built, ok := r.registries.Load(user); ok && built.(userRegistry).set == set {
		return built.(userRegistry).registry, nil
	}
	registry, err := tools.New(r.ctx, set.Unavailable, slices.Concat(r.providers, set.Providers)...)
	if err != nil {
		return nil, fmt.Errorf("resources: the tools of the user: %w", err)
	}
	r.registries.Store(user, userRegistry{set: set, registry: registry})
	return registry, nil
}

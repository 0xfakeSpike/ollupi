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
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Config struct {
	// One per connection; the first is the default.
	Models      []llm.Model
	Connections map[string]llm.Client
	// Runs a user's turns on their own key and quota; it should return the same client each time.
	ClientFor func(user, connection string) (llm.Client, error)
	ToolsFor  func(user string) (*tools.Set, error)
}

type Resources struct {
	cfg        Config
	ctx        context.Context
	providers  []tools.Provider
	registry   *tools.Registry
	registries sync.Map
}

type userRegistry struct {
	set      *tools.Set
	registry *tools.Registry
}

// A user's own tools connect within ctx.
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

func (r *Resources) Models() ([]llm.Model, string) {
	models := make([]llm.Model, 0, len(r.cfg.Models))
	for _, m := range r.cfg.Models {
		models = append(models, m.Clone())
	}
	return models, r.cfg.Models[0].Connection
}

func (r *Resources) Model(connection string) (llm.Model, error) {
	i := slices.IndexFunc(r.cfg.Models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", connection)
	}
	return r.cfg.Models[i].Clone(), nil
}

func (r *Resources) Window(m llm.Model) (*contextmanager.Manager, error) {
	limits, err := r.cfg.Connections[m.Connection].Limits(m.Name)
	if err != nil {
		return nil, err
	}
	return contextmanager.New(m.Name, limits), nil
}

func (r *Resources) Client(user, connection string) (llm.Client, error) {
	if r.cfg.ClientFor != nil {
		return r.cfg.ClientFor(user, connection)
	}
	return r.cfg.Connections[connection], nil
}

// Settings shared by all may then name tools one user lacks.
func (r *Resources) Shared() bool { return r.cfg.ToolsFor != nil }

func (r *Resources) Tools() *tools.Registry { return r.registry }

// The first call may wait for the user's servers to connect.
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

func (r *Resources) ConversationModel(last *db.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && last != nil {
		connection = last.Model.Connection
	}
	if connection == "" {
		_, connection = r.Models()
	}
	return r.Model(connection)
}

func (r *Resources) MainRole(model llm.Model) (role.Name, error) {
	window, err := r.Window(model)
	if err != nil {
		return "", err
	}
	return role.ForMain(window.InputTokens()), nil
}

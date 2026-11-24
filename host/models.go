package host

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/store"
)

// Dial makes the client that calls one model of the deployment. The default
// speaks the provider's API at its base URL with its key; a deployment
// replaces it to route calls elsewhere, and tests to answer them.
type Dial func(p account.ModelProvider, m account.ProviderModel) (llm.Client, error)

func dial(p account.ModelProvider, m account.ProviderModel) (llm.Client, error) {
	limits := map[string]llm.Limits{m.Name: m.Limits}
	switch p.API {
	case account.OpenAI:
		return llm.NewOpenAI(p.BaseURL, p.APIKey, m.Calls, limits), nil
	case account.Anthropic:
		return llm.NewAnthropic(p.BaseURL, p.APIKey, m.Calls, limits), nil
	case account.Gemini:
		return llm.NewGemini(p.BaseURL, p.APIKey, m.Calls, limits)
	}
	return nil, fmt.Errorf("model provider %s: unknown API %q", p.ID, p.API)
}

// models is the deployment's catalog: the providers and models every user's
// turns run on, each model with the one client that calls it, which caps
// the calls to it at once across users. Which model a user starts with and
// learns with is theirs, in their settings. The catalog can be replaced while
// the host runs; a turn already calling a model finishes on its client.
type models struct {
	ctx  context.Context
	st   store.Store
	dial Dial
	// clientFor, when set, picks a user's client instead of the catalog's,
	// as a deployment that bills each user's own account would.
	clientFor func(user, connection string) (llm.Client, error)

	mu      sync.RWMutex
	catalog []account.ModelProvider
	clients map[string]dialled
}

// dialled is a model's client and what it was made from, so a replaced
// catalog keeps the clients of the models it did not change.
type dialled struct {
	client   llm.Client
	provider account.ModelProvider
	model    account.ProviderModel
}

func newModels(ctx context.Context, st store.Store, catalog []account.ModelProvider, d Dial, clientFor func(user, connection string) (llm.Client, error)) (*models, error) {
	if d == nil {
		d = dial
	}
	ms := &models{ctx: ctx, st: st, dial: d, clientFor: clientFor, clients: map[string]dialled{}}
	return ms, ms.set(catalog)
}

// set replaces the catalog; a catalog that does not check, or a model that
// cannot be dialled, leaves the one in use.
func (ms *models) set(catalog []account.ModelProvider) error {
	if err := account.CheckCatalog(catalog); err != nil {
		return fmt.Errorf("host: the models: %w", err)
	}
	ms.mu.RLock()
	before := ms.clients
	ms.mu.RUnlock()
	clients := map[string]dialled{}
	for _, p := range catalog {
		for _, m := range p.Models {
			id := account.Connection(p.ID, m.Name)
			if old, ok := before[id]; ok && sameClient(old, p, m) {
				clients[id] = old
				continue
			}
			c, err := ms.dial(p, m)
			if err != nil {
				return fmt.Errorf("host: the models: %s: %w", id, err)
			}
			clients[id] = dialled{client: c, provider: p, model: m}
		}
	}
	ms.mu.Lock()
	ms.catalog, ms.clients = clone(catalog), clients
	ms.mu.Unlock()
	return nil
}

// sameClient says whether a model's client still serves it: one made for
// the same account, model, cap on calls and limits.
func sameClient(old dialled, p account.ModelProvider, m account.ProviderModel) bool {
	o := old.provider
	return o.API == p.API && o.BaseURL == p.BaseURL && o.APIKey == p.APIKey &&
		old.model.Name == m.Name && old.model.Calls == m.Calls && old.model.Limits == m.Limits
}

func clone(catalog []account.ModelProvider) []account.ModelProvider {
	out := make([]account.ModelProvider, len(catalog))
	for i, p := range catalog {
		p.Models = slices.Clone(p.Models)
		out[i] = p
	}
	return out
}

// Providers are the catalog as it is now, keys included: what leaves the
// process for a client drops them.
func (ms *models) Providers() []account.ModelProvider {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return clone(ms.catalog)
}

func (ms *models) Limits(_ string, m llm.Model) (llm.Limits, error) {
	ms.mu.RLock()
	d, ok := ms.clients[m.Connection]
	ms.mu.RUnlock()
	if !ok {
		return llm.Limits{}, unavailable(m.Connection)
	}
	return d.model.Limits, nil
}

func (ms *models) Client(user string, m llm.Model) (llm.Client, error) {
	ms.mu.RLock()
	d, ok := ms.clients[m.Connection]
	ms.mu.RUnlock()
	if !ok {
		return nil, unavailable(m.Connection)
	}
	if ms.clientFor != nil {
		return ms.clientFor(user, m.Connection)
	}
	return d.client, nil
}

// memory is the model that learns the user's memories: the one they chose,
// while the deployment serves it.
func (ms *models) memory(user string) (llm.Model, bool) {
	var s account.Settings
	if err := ms.st.Read(ms.ctx, func(r store.Reader) (err error) { s, err = r.Settings(user); return err }); err != nil {
		slog.Error("memory: the user's learning model was not read", "user", user, "err", err)
		return llm.Model{}, false
	}
	providers := ms.Providers()
	p, m, ok := account.Find(providers, s.LearnModel)
	if !ok {
		return llm.Model{}, false
	}
	return p.Model(m), true
}

func unavailable(connection string) error {
	return fmt.Errorf("model %q is no longer served; select another model for a new turn", connection)
}

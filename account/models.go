package account

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

var (
	ErrInvalidModelProvider = errors.New("invalid model provider")
	// ErrUnknownModel is a choice of a model the deployment does not serve.
	ErrUnknownModel = errors.New("unknown model")
)

// ModelAPI is the API a provider speaks, which picks the client that calls it.
type ModelAPI string

const (
	OpenAI    ModelAPI = "openai"
	Anthropic ModelAPI = "anthropic"
	Gemini    ModelAPI = "gemini"
)

var ModelAPIs = []ModelAPI{OpenAI, Anthropic, Gemini}

// ModelProvider is one API account of a user and the models they run through
// it. It is the user's own: another user never sees or shares it. A turn names
// a model by its connection, the provider's ID and the model's name.
type ModelProvider struct {
	ID      string
	API     ModelAPI
	BaseURL string
	// APIKey is empty for an API that takes none.
	APIKey string
	Models []ProviderModel
}

// ProviderModel is one model of a provider. Calls caps the requests to it
// that run at once; 0 is the client's default.
type ProviderModel struct {
	Name     string `json:"name"`
	Calls    int    `json:"calls,omitempty"`
	Thinking *bool  `json:"thinking,omitempty"`
	// Images says the model accepts images; none is sent to one without.
	Images bool       `json:"images,omitempty"`
	Limits llm.Limits `json:"limits"`
}

const maxProviderID = 64

// Connection is the key a turn selects a model by.
func Connection(provider, model string) string { return provider + "/" + model }

func (p ModelProvider) Check() error {
	switch {
	case strings.TrimSpace(p.ID) == "" || p.ID != strings.TrimSpace(p.ID) || len([]rune(p.ID)) > maxProviderID:
		return fmt.Errorf("%w: the name %q must be 1 to %d characters without surrounding spaces", ErrInvalidModelProvider, p.ID, maxProviderID)
	case strings.Contains(p.ID, "/"):
		return fmt.Errorf("%w: the name %q must not contain '/', which separates it from a model's name", ErrInvalidModelProvider, p.ID)
	case !slices.Contains(ModelAPIs, p.API):
		return fmt.Errorf("%w: %s: the API must be one of %q, got %q", ErrInvalidModelProvider, p.ID, ModelAPIs, p.API)
	case p.API == Gemini && p.APIKey == "":
		return fmt.Errorf("%w: %s: the Gemini API needs a key", ErrInvalidModelProvider, p.ID)
	case len(p.Models) == 0:
		return fmt.Errorf("%w: %s serves no models", ErrInvalidModelProvider, p.ID)
	}
	for i, m := range p.Models {
		switch {
		case strings.TrimSpace(m.Name) == "":
			return fmt.Errorf("%w: %s has a model without a name", ErrInvalidModelProvider, p.ID)
		case slices.ContainsFunc(p.Models[:i], func(o ProviderModel) bool { return o.Name == m.Name }):
			return fmt.Errorf("%w: %s lists %s twice", ErrInvalidModelProvider, p.ID, m.Name)
		case m.Calls < 0:
			return fmt.Errorf("%w: %s: calls must not be negative", ErrInvalidModelProvider, Connection(p.ID, m.Name))
		}
		if err := m.Limits.Check(); err != nil {
			return fmt.Errorf("%w: %s: limits: %w", ErrInvalidModelProvider, Connection(p.ID, m.Name), err)
		}
	}
	return nil
}

// Model is how a turn selects one of the provider's models.
func (p ModelProvider) Model(m ProviderModel) llm.Model {
	return llm.Model{Name: m.Name, Connection: Connection(p.ID, m.Name), Thinking: m.Thinking, Images: m.Images}
}

// Connections are every model of the providers, as turns select them.
func Connections(providers []ModelProvider) []string {
	var out []string
	for _, p := range providers {
		for _, m := range p.Models {
			out = append(out, Connection(p.ID, m.Name))
		}
	}
	return out
}

// CheckCatalog checks a deployment's providers: each on its own, and their
// names, which must tell them apart.
func CheckCatalog(providers []ModelProvider) error {
	for i, p := range providers {
		if err := p.Check(); err != nil {
			return err
		}
		if slices.ContainsFunc(providers[:i], func(o ModelProvider) bool { return o.ID == p.ID }) {
			return fmt.Errorf("%w: %s is listed twice", ErrInvalidModelProvider, p.ID)
		}
	}
	return nil
}

// Models are the models the providers serve, in order, and the connection a
// conversation starts with: the settings' model while it is one of them,
// else the first; none without models.
func Models(providers []ModelProvider, s Settings) ([]llm.Model, string) {
	var list []llm.Model
	for _, p := range providers {
		for _, m := range p.Models {
			list = append(list, p.Model(m))
		}
	}
	if len(list) == 0 {
		return nil, ""
	}
	if slices.ContainsFunc(list, func(m llm.Model) bool { return m.Connection == s.Model }) {
		return list, s.Model
	}
	return list, list[0].Connection
}

// Find is the provider model a connection names.
func Find(providers []ModelProvider, connection string) (ModelProvider, ProviderModel, bool) {
	for _, p := range providers {
		for _, m := range p.Models {
			if Connection(p.ID, m.Name) == connection {
				return p, m, true
			}
		}
	}
	return ModelProvider{}, ProviderModel{}, false
}

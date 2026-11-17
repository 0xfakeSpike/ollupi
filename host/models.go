package host

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// models are the deployment's models, each on its connection, and the client each user calls one through.
type models struct {
	list        []llm.Model
	connections map[string]llm.Client
	clientFor   func(user, connection string) (llm.Client, error)
}

func newModels(list []llm.Model, connections map[string]llm.Client, clientFor func(user, connection string) (llm.Client, error)) (models, error) {
	if len(list) == 0 {
		return models{}, errors.New("host: at least one model is required")
	}
	for i, m := range list {
		if m.Connection == "" || strings.TrimSpace(m.Name) == "" {
			return models{}, errors.New("host: every model needs a connection and a name")
		}
		if slices.ContainsFunc(list[:i], func(o llm.Model) bool { return o.Connection == m.Connection }) {
			return models{}, fmt.Errorf("host: connection %q is listed twice", m.Connection)
		}
		if connections[m.Connection] == nil {
			return models{}, fmt.Errorf("host: connection %q has no client", m.Connection)
		}
	}
	return models{list: list, connections: connections, clientFor: clientFor}, nil
}

func (ms models) Models() []llm.Model {
	out := make([]llm.Model, len(ms.list))
	for i, m := range ms.list {
		out[i] = m.Clone()
	}
	return out
}

func (ms models) connection(m llm.Model) (llm.Client, error) {
	c := ms.connections[m.Connection]
	if c == nil {
		return nil, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", m.Connection)
	}
	return c, nil
}

func (ms models) Limits(m llm.Model) (llm.Limits, error) {
	c, err := ms.connection(m)
	if err != nil {
		return llm.Limits{}, err
	}
	return c.Limits(m.Name)
}

func (ms models) Client(user string, m llm.Model) (llm.Client, error) {
	if ms.clientFor != nil {
		return ms.clientFor(user, m.Connection)
	}
	return ms.connection(m)
}

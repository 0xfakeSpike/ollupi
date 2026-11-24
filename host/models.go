package host

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// UserModels are one user's complete model configuration: the models they
// may run, the clients that call them, and the model that learns their memory.
type UserModels struct {
	User        string
	Models      []llm.Model
	Connections map[string]llm.Client
	Memory      *llm.Model
}

// models finds the models one user may run. There is no shared or fallback
// set: a user without a configuration has none.
type models struct {
	clientFor func(user, connection string) (llm.Client, error)
	mu        sync.RWMutex
	users     map[string]userModels
}

type userModels struct {
	list        []llm.Model
	connections map[string]llm.Client
	memory      *llm.Model
}

func newModels(clientFor func(user, connection string) (llm.Client, error), users []UserModels) (*models, error) {
	byUser, err := usersOf(users)
	if err != nil {
		return nil, err
	}
	return &models{users: byUser, clientFor: clientFor}, nil
}

func (ms *models) set(users []UserModels) error {
	byUser, err := usersOf(users)
	if err != nil {
		return err
	}
	ms.mu.Lock()
	ms.users = byUser
	ms.mu.Unlock()
	return nil
}

func usersOf(users []UserModels) (map[string]userModels, error) {
	byUser := make(map[string]userModels, len(users))
	for _, u := range users {
		if strings.TrimSpace(u.User) == "" {
			return nil, errors.New("host: a user of its own models needs a name")
		}
		if _, seen := byUser[u.User]; seen {
			return nil, fmt.Errorf("host: user %q is listed twice", u.User)
		}
		set, err := newUserModels("the models of user "+u.User, u.Models, u.Connections)
		if err != nil {
			return nil, err
		}
		if u.Memory != nil && set.connections[u.Memory.Connection] == nil {
			return nil, fmt.Errorf("host: the models of user %q: learning model connection %q is not among theirs", u.User, u.Memory.Connection)
		}
		set.memory = u.Memory
		byUser[u.User] = set
	}
	return byUser, nil
}

func newUserModels(what string, list []llm.Model, connections map[string]llm.Client) (userModels, error) {
	if len(list) == 0 {
		return userModels{}, nil
	}
	for i, m := range list {
		if m.Connection == "" || strings.TrimSpace(m.Name) == "" {
			return userModels{}, fmt.Errorf("host: %s: every model needs a connection and a name", what)
		}
		if slices.ContainsFunc(list[:i], func(o llm.Model) bool { return o.Connection == m.Connection }) {
			return userModels{}, fmt.Errorf("host: %s: connection %q is listed twice", what, m.Connection)
		}
		if connections[m.Connection] == nil {
			return userModels{}, fmt.Errorf("host: %s: connection %q has no client", what, m.Connection)
		}
	}
	return userModels{list: list, connections: connections}, nil
}

func (ms *models) of(user string) userModels {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.users[user]
}

func (ms *models) memory(user string) (llm.Model, bool) {
	m := ms.of(user).memory
	if m == nil {
		return llm.Model{}, false
	}
	return *m, true
}

func (ms *models) Models(user string) []llm.Model {
	set := ms.of(user)
	out := make([]llm.Model, len(set.list))
	for i, m := range set.list {
		out[i] = m.Clone()
	}
	return out
}

func (ms *models) Limits(user string, m llm.Model) (llm.Limits, error) {
	set := ms.of(user)
	c := set.connections[m.Connection]
	if c == nil {
		return llm.Limits{}, fmt.Errorf("connection %q is not this profile's; add it in Settings or select another model for a new turn", m.Connection)
	}
	return c.Limits(m.Name)
}

func (ms *models) Client(user string, m llm.Model) (llm.Client, error) {
	if ms.clientFor != nil {
		return ms.clientFor(user, m.Connection)
	}
	set := ms.of(user)
	c := set.connections[m.Connection]
	if c == nil {
		return nil, fmt.Errorf("connection %q is not this profile's; add it in Settings or select another model for a new turn", m.Connection)
	}
	return c, nil
}

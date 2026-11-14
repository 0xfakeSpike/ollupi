package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (a *Agent) checkModels() error {
	if len(a.cfg.Models) == 0 {
		return fmt.Errorf("agent %s: at least one model is required", a.cfg.Name)
	}
	for i, m := range a.cfg.Models {
		if m.Connection == "" || strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("agent: every model needs a connection and a name")
		}
		if slices.ContainsFunc(a.cfg.Models[:i], func(o llm.Model) bool { return o.Connection == m.Connection }) {
			return fmt.Errorf("agent: connection %q is listed twice", m.Connection)
		}
		if a.cfg.Connections[m.Connection] == nil {
			return fmt.Errorf("agent: connection %q has no client", m.Connection)
		}
	}
	return nil
}

func (a *Agent) windowFor(m llm.Model) (*contextmanager.Manager, error) {
	limits, err := a.cfg.Connections[m.Connection].Limits(m.Name)
	if err != nil {
		return nil, err
	}
	return contextmanager.New(m.Name, limits), nil
}

// mainRole is the main role of a turn on the model, by how much input it takes.
func (a *Agent) mainRole(model llm.Model) (role.Role, error) {
	window, err := a.windowFor(model)
	if err != nil {
		return role.Role{}, err
	}
	return role.ForMain(window.InputTokens()), nil
}

func (a *Agent) clientFor(user, connection string) (llm.Client, error) {
	if a.cfg.ClientFor != nil {
		return a.cfg.ClientFor(user, connection)
	}
	return a.cfg.Connections[connection], nil
}

func (a *Agent) checkPromptFits() error {
	checker, err := permission.New(a.layers(repo.WorkspaceRow{}), a.cfg.Tools, a.registry)
	if err != nil {
		return err
	}
	descriptors := checker.Tools()
	var catalog []skills.Skill
	if a.skills != nil {
		catalog = a.skills.All()
	}
	for _, m := range a.cfg.Models {
		window, err := a.windowFor(m)
		if err != nil {
			return err
		}
		main := role.ForMain(window.InputTokens())
		offered, deferred := main.Offer(descriptors, window, nil)
		system := main.System(role.Input{Agent: role.AgentPrompt(a.cfg.Name, a.cfg.Prompt), Skills: catalog, SkillTokens: window.SkillBudget(), Deferred: deferred})
		if err := window.Fits(system, role.Specs(offered)); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) ListModels() ([]llm.Model, string) {
	models := make([]llm.Model, 0, len(a.cfg.Models))
	for _, m := range a.cfg.Models {
		models = append(models, m.Clone())
	}
	return models, a.cfg.Models[0].Connection
}

func (a *Agent) modelOn(connection string) (llm.Model, error) {
	i := slices.IndexFunc(a.cfg.Models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", connection)
	}
	return a.cfg.Models[i].Clone(), nil
}

func (a *Agent) conversationModel(last *repo.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && last != nil {
		connection = last.Model.Connection
	}
	if connection == "" {
		connection = a.cfg.Models[0].Connection
	}
	model, err := a.modelOn(connection)
	if err != nil {
		return llm.Model{}, fmt.Errorf("agent: %w: %v", ErrInvalidMessage, err)
	}
	return model, nil
}

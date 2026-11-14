package orchestrator

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

func (o *Orchestrator) checkModels() error {
	if len(o.cfg.Models) == 0 {
		return fmt.Errorf("agent %s: at least one model is required", o.cfg.Name)
	}
	for i, m := range o.cfg.Models {
		if m.Connection == "" || strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("orchestrator: every model needs a connection and a name")
		}
		if slices.ContainsFunc(o.cfg.Models[:i], func(o llm.Model) bool { return o.Connection == m.Connection }) {
			return fmt.Errorf("orchestrator: connection %q is listed twice", m.Connection)
		}
		if o.cfg.Connections[m.Connection] == nil {
			return fmt.Errorf("orchestrator: connection %q has no client", m.Connection)
		}
	}
	return nil
}

func (o *Orchestrator) windowFor(m llm.Model) (*contextmanager.Manager, error) {
	limits, err := o.cfg.Connections[m.Connection].Limits(m.Name)
	if err != nil {
		return nil, err
	}
	return contextmanager.New(m.Name, limits), nil
}

// mainRole is the main role of a turn on the model, by how much input it takes.
func (o *Orchestrator) mainRole(model llm.Model) (role.Role, error) {
	window, err := o.windowFor(model)
	if err != nil {
		return role.Role{}, err
	}
	return role.ForMain(window.InputTokens()), nil
}

func (o *Orchestrator) clientFor(user, connection string) (llm.Client, error) {
	if o.cfg.ClientFor != nil {
		return o.cfg.ClientFor(user, connection)
	}
	return o.cfg.Connections[connection], nil
}

func (o *Orchestrator) checkPromptFits() error {
	checker, err := permission.New(o.layers(repo.WorkspaceRow{}), o.cfg.Tools, o.registry)
	if err != nil {
		return err
	}
	descriptors := checker.Tools()
	var catalog []skills.Skill
	if o.skills != nil {
		catalog = o.skills.All()
	}
	for _, m := range o.cfg.Models {
		window, err := o.windowFor(m)
		if err != nil {
			return err
		}
		main := role.ForMain(window.InputTokens())
		offered, deferred := main.Offer(descriptors, window, nil)
		system := main.System(role.Input{Agent: role.AgentPrompt(o.cfg.Name, o.cfg.Prompt), Skills: catalog, SkillTokens: window.SkillBudget(), Deferred: deferred})
		if err := window.Fits(system, role.Specs(offered)); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) ListModels() ([]llm.Model, string) {
	models := make([]llm.Model, 0, len(o.cfg.Models))
	for _, m := range o.cfg.Models {
		models = append(models, m.Clone())
	}
	return models, o.cfg.Models[0].Connection
}

func (o *Orchestrator) modelOn(connection string) (llm.Model, error) {
	i := slices.IndexFunc(o.cfg.Models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", connection)
	}
	return o.cfg.Models[i].Clone(), nil
}

func (o *Orchestrator) conversationModel(last *repo.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && last != nil {
		connection = last.Model.Connection
	}
	if connection == "" {
		connection = o.cfg.Models[0].Connection
	}
	model, err := o.modelOn(connection)
	if err != nil {
		return llm.Model{}, fmt.Errorf("orchestrator: %w: %v", ErrInvalidMessage, err)
	}
	return model, nil
}

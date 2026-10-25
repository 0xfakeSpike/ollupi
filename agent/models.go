package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/agent/systemprompt"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

type ModelList struct {
	Data    []llm.Model `json:"data"`
	Default string      `json:"default"`
}

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

func (a *Agent) contextsFor(m llm.Model) (*contextmanager.Manager, error) {
	conn := a.cfg.Connections[m.Connection]
	if conn == nil {
		return nil, fmt.Errorf("connection %q has no client", m.Connection)
	}
	limits, err := conn.Limits(m.Name)
	if err != nil {
		return nil, err
	}
	return contextmanager.New(m.Name, limits), nil
}

func (a *Agent) checkBudgets() error {
	checker, err := permission.New(a.cfg.Settings, a.cfg.Tools, a.registry)
	if err != nil {
		return err
	}
	descriptors := checker.Tools()
	var catalog []skills.Skill
	if a.skills != nil {
		catalog = a.skills.All()
	}
	for _, m := range a.cfg.Models {
		contexts, err := a.contextsFor(m)
		if err != nil {
			return err
		}
		system := systemprompt.Build(systemprompt.Input{Agent: systemprompt.AgentPrompt(a.cfg.Name, a.cfg.Prompt), Skills: catalog, SkillTokens: contexts.SkillBudget()})
		if err := contexts.Fits(system, runtime.Specs(descriptors)); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) ListModels() ModelList {
	data := make([]llm.Model, 0, len(a.cfg.Models))
	for _, m := range a.cfg.Models {
		data = append(data, m.Clone())
	}
	return ModelList{Data: data, Default: a.cfg.Models[0].Connection}
}

func (a *Agent) modelOn(connection string) (llm.Model, error) {
	i := slices.IndexFunc(a.cfg.Models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("connection %q is unavailable; restore it in Settings or select another model for a new turn", connection)
	}
	return a.cfg.Models[i].Clone(), nil
}

func (a *Agent) conversationModel(turns []repo.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && len(turns) > 0 {
		connection = turns[len(turns)-1].Model.Connection
	}
	if connection == "" {
		connection = a.cfg.Models[0].Connection
	}
	return a.modelOn(connection)
}

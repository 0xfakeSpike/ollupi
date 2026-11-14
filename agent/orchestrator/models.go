package orchestrator

import (
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// mainRole is the main role of a turn on the model, by how much input it takes.
func (o *Orchestrator) mainRole(model llm.Model) (role.Role, error) {
	window, err := o.res.Window(model)
	if err != nil {
		return role.Role{}, err
	}
	return role.ForMain(window.InputTokens()), nil
}

func (o *Orchestrator) checkPromptFits() error {
	checker, err := permission.New(permission.Base(o.cfg.Settings, o.res.Shared(), db.WorkspaceRow{}), o.cfg.Tools, o.res.Tools())
	if err != nil {
		return err
	}
	descriptors := checker.Tools()
	models, _ := o.res.Models()
	for _, m := range models {
		window, err := o.res.Window(m)
		if err != nil {
			return err
		}
		main := role.ForMain(window.InputTokens())

		offered, deferred := main.Offer(descriptors, window, nil)
		system, err := main.System(role.Input{
			Name: o.cfg.Name, Prompt: o.cfg.Prompt, SkillsDir: o.cfg.SkillsDir, SkillTokens: window.SkillBudget(), Deferred: deferred,
		})
		if err != nil {
			return err
		}
		if err := window.Fits(system, role.Specs(offered)); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) ListModels() ([]llm.Model, string) { return o.res.Models() }

func (o *Orchestrator) conversationModel(last *db.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && last != nil {
		connection = last.Model.Connection
	}
	if connection == "" {
		_, connection = o.res.Models()
	}
	model, err := o.res.Model(connection)
	if err != nil {
		return llm.Model{}, fmt.Errorf("orchestrator: %w: %v", ErrInvalidMessage, err)
	}
	return model, nil
}

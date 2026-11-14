package orchestrator

import (
	"fmt"
	"strings"

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
		return llm.Model{}, fmt.Errorf("orchestrator: %w", err)
	}
	return model, nil
}

package agent

import (
	"context"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/agent/systemprompt"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func (a *Agent) step(ctx context.Context, view repo.ThreadView, turn repo.TurnRow) (bool, repo.Usage, error) {
	contexts, err := a.contextsFor(turn.Model)
	if err != nil {
		return false, repo.Usage{}, err
	}
	w, err := a.workspaceOf(view.Thread.WorkspaceID)
	if err != nil {
		return false, repo.Usage{}, err
	}
	settings, err := a.threadSettings(view.Thread, w)
	if err != nil {
		return false, repo.Usage{}, err
	}
	checker, err := permission.New(settings, a.cfg.Tools, a.registry)
	if err != nil {
		return false, repo.Usage{}, err
	}
	descriptors := checker.Tools()
	var history, items []repo.ItemRow
	for _, it := range view.Items {
		if it.TurnID == turn.ID {
			items = append(items, it)
		} else {
			history = append(history, it)
		}
	}
	rt := &runtime.Runtime{
		LLM:         a.cfg.Connections[turn.Model.Connection],
		Registry:    a.registry,
		Skills:      a.skills,
		Contexts:    contexts,
		Memory:      a.cfg.Memory,
		Turn:        turn,
		Caller:      tools.Caller{ThreadID: view.Thread.ID, WorkspaceID: string(view.Thread.WorkspaceID), Root: w.RootPath, Subagent: view.Thread.ParentThreadID != ""},
		Prompt:      systemprompt.AgentPrompt(a.cfg.Name, a.cfg.Prompt),
		Tools:       descriptors,
		Permissions: checker,
		History:     history,
		Items:       items,
		Record:      a.recorder(view.Thread, turn),
		Delta: func(it repo.ItemRow, text string) {
			a.emit(Event{Kind: EventItemDelta, Thread: view.Thread, Turn: turn, Item: &it, Delta: text})
		},
	}
	return rt.Step(ctx)
}

func (a *Agent) recorder(t repo.ThreadRow, turn repo.TurnRow) func(repo.ItemRow) (repo.ItemRow, error) {
	return func(it repo.ItemRow) (repo.ItemRow, error) {
		stored, isNew, err := a.cfg.DB.RecordItem(t.ID, it)
		if err != nil {
			return repo.ItemRow{}, err
		}
		a.emitItem(t, turn, stored, isNew)
		return stored, nil
	}
}

func findTurn(view repo.ThreadView, turnID string) (repo.TurnRow, bool) {
	for _, t := range view.Turns {
		if t.ID == turnID {
			return t, true
		}
	}
	return repo.TurnRow{}, false
}

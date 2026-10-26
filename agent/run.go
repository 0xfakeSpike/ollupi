package agent

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/agent/systemprompt"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func (a *Agent) runTurn(ctx context.Context, view repo.ThreadView, turn repo.TurnRow) (bool, repo.Usage, error) {
	contexts, err := a.contextsFor(turn.Model)
	if err != nil {
		return false, repo.Usage{}, err
	}
	checker, w, err := a.permissions(view.Thread)
	if err != nil {
		return false, repo.Usage{}, err
	}
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
		Tools:       checker.Tools(),
		Permissions: checker,
		History:     history,
		Items:       items,
		Record:      a.recorder(view.Thread, turn),
		Delta: func(it repo.ItemRow, text string) {
			a.emit(Event{Kind: EventItemDelta, Thread: view.Thread, Turn: turn, Item: &it, Delta: text})
		},
		Reload: func() ([]repo.ItemRow, *permission.Checker, error) {
			items, err := a.cfg.DB.TurnItems(view.Thread.ID, turn.ID)
			if err != nil {
				return nil, nil, err
			}
			t, err := a.cfg.DB.ThreadRow(view.Thread.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("agent: thread %s: %w", view.Thread.ID, err)
			}
			checker, _, err := a.permissions(t)
			return items, checker, err
		},
	}
	return rt.Run(ctx)
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

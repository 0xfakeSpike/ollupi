package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// turnHost is what the agent gives a run of one turn: runtime.Host for the turn's thread.
type turnHost struct {
	a        *Agent
	t        repo.ThreadRow
	turn     repo.TurnRow
	role     role.Role
	caller   tools.Caller
	registry *tools.Registry
}

func (h turnHost) Record(it repo.ItemRow) (repo.ItemRow, error) {
	stored, isNew, err := h.a.cfg.DB.RecordItem(h.t.ID, it)
	if err != nil {
		return repo.ItemRow{}, err
	}
	h.a.emitItem(h.t, h.turn, stored, isNew)
	return stored, nil
}

func (h turnHost) Delta(it repo.ItemRow, text string) {
	h.a.emit(Event{Kind: EventItemDelta, Thread: h.t, Turn: h.turn, Item: &it, Delta: text})
}

func (h turnHost) Reload() ([]repo.ItemRow, *permission.Checker, error) {
	items, err := h.a.cfg.DB.TurnItems(h.turn.ID)
	if err != nil {
		return nil, nil, err
	}
	current, err := h.a.cfg.DB.ThreadRow(h.t.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: thread %s: %w", h.t.ID, err)
	}
	checker, _, err := h.a.permissions(current, h.role)
	return items, checker, err
}

func (h turnHost) Settings() (repo.SettingsRow, error) { return h.a.cfg.DB.Settings(h.t.UserID) }

func (h turnHost) Skills() []skills.Skill {
	if h.a.skills == nil {
		return nil
	}
	return h.a.skills.All()
}

func (h turnHost) Memory(budget int) (*memory.Snapshot, error) {
	if h.a.cfg.Memory == nil {
		return nil, nil
	}
	return h.a.cfg.Memory.Snapshot(h.t.UserID, string(h.t.WorkspaceID), budget)
}

func (h turnHost) Attachment(ctx context.Context, path string) ([]byte, error) {
	if h.a.cfg.Attachments == nil {
		return nil, errors.New("the conversation has attachments, but this agent keeps none")
	}
	return h.a.cfg.Attachments.Get(ctx, path)
}

func (h turnHost) Background(it repo.ItemRow, args map[string]any) tools.Result {
	return h.a.startBackground(h.t, h.caller, h.registry, it, args)
}

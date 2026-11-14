package orchestrator

import (
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// turnHost is what the orchestrator does for a run of one turn: runtime.Host for the turn's thread.
type turnHost struct {
	o        *Orchestrator
	t        db.ThreadRow
	turn     db.TurnRow
	registry *tools.Registry
}

func (h turnHost) Notify(it db.ItemRow, isNew bool) { h.o.emitItem(h.t, h.turn, it, isNew) }

func (h turnHost) Delta(it db.ItemRow, text string) {
	h.o.emit(Event{Kind: EventItemDelta, Thread: h.t, Turn: h.turn, Item: &it, Delta: text})
}

func (h turnHost) Background(caller tools.Caller, it db.ItemRow, args map[string]any) tools.Result {
	return h.o.startBackground(h.t, caller, h.registry, it, args)
}

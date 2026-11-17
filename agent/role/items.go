package role

import (
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (rt *Runtime) conversation() []db.ItemRow { return slices.Concat(rt.history, rt.items) }

func (rt *Runtime) keep(stored db.ItemRow) {
	for i := range rt.items {
		if rt.items[i].ID == stored.ID {
			rt.items[i] = stored
			return
		}
	}
	rt.items = append(rt.items, stored)
}

func (rt *Runtime) record(it db.ItemRow) (db.ItemRow, error) {
	stored, isNew, err := rt.store.RecordItem(rt.thread.ID, it)
	if err != nil {
		return db.ItemRow{}, fmt.Errorf("role: record %s item: %w", it.Type, err)
	}
	rt.events.Item(rt.thread, rt.turn, stored, isNew)
	rt.keep(stored)
	return stored, nil
}

func (rt *Runtime) complete(it db.ItemRow, status db.ItemStatus) (db.ItemRow, error) {
	now := time.Now().UTC()
	it.Status = status
	it.CompletedAt = &now
	return rt.record(it)
}

func withError(it db.ItemRow, text string) db.ItemRow {
	it.Error = text
	return it
}

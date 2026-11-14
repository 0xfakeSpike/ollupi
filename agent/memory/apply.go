package memory

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

func commit(store *db.DB, b *Batch, ops []Operation, report db.MemoryReport) error {
	return store.Tx(func(tx *db.DB) error {
		current, err := tx.Memories(b.UserID, b.Scope, b.WorkspaceID, true)
		if err != nil {
			return err
		}
		s := &applyState{
			tx: tx, batch: b, now: time.Now().UTC(),
			byID: map[string]db.MemoryRow{}, byTopic: map[topicKey]string{}, blocked: forgottenSources(current),
		}
		for _, e := range current {
			s.byID[e.ID] = e
			s.byTopic[topicKey{e.Scope, e.Topic}] = e.ID
		}
		if err := s.apply(ops); err != nil {
			return err
		}
		return tx.AdvanceMemory(b.UserID, b.Cursor, s.now, report)
	})
}

type applyState struct {
	tx      *db.DB
	batch   *Batch
	now     time.Time
	byID    map[string]db.MemoryRow
	byTopic map[topicKey]string
	blocked map[string]bool
}

func (s *applyState) apply(ops []Operation) error {
	for _, op := range ops {
		e, ok, err := s.target(op)
		if err != nil {
			return err
		}
		if !ok || s.blockedBy(op.Sources) {
			continue
		}
		if op.Action == "retire" {
			e.Status = db.MemoryRetired
		} else {
			e.Text, e.Kind, e.Status = op.Text, op.Kind, db.MemoryActive
		}
		e.Sources, e.UpdatedAt = op.Sources, s.now
		if err := s.tx.SaveMemory(e); err != nil {
			return err
		}
		s.byID[e.ID] = e
		s.byTopic[topicKey{e.Scope, e.Topic}] = e.ID
	}
	return nil
}

type topicKey struct {
	scope db.MemoryScope
	topic string
}

func (s *applyState) target(op Operation) (db.MemoryRow, bool, error) {
	var e db.MemoryRow
	if op.Action == "add" {
		added := db.MemoryRow{ID: uuid.NewString(), UserID: s.batch.UserID, Scope: s.batch.Scope, WorkspaceID: s.batch.WorkspaceID, Topic: op.Topic, CreatedAt: s.now}
		if op.Scope == scopeGlobal {
			added.Scope, added.WorkspaceID, added.OriginWorkspaceID = db.MemoryGlobal, "", s.batch.WorkspaceID
		}
		id, exists := s.byTopic[topicKey{added.Scope, op.Topic}]
		if !exists {
			return added, true, nil
		}
		e = s.byID[id]
	} else {
		var ok bool
		if e, ok = s.byID[op.ID]; !ok {
			return db.MemoryRow{}, false, fmt.Errorf("memory %s disappeared while the batch was being extracted", op.ID)
		}
	}
	if e.Status == db.MemoryForgotten || e.UserEdited {
		return db.MemoryRow{}, false, nil
	}
	return e, true, nil
}

func (s *applyState) blockedBy(sources []db.MemorySource) bool {
	for _, src := range sources {
		if s.blocked[sourceKey(src)] {
			return true
		}
	}
	return false
}

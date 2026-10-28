package memory

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func commit(db *repo.DB, b *Batch, ops []Operation, report repo.MemoryReport) error {
	return db.Tx(func(tx *repo.DB) error {
		current, err := tx.Memories(b.Scope, b.WorkspaceID, true)
		if err != nil {
			return err
		}
		s := &applyState{
			tx: tx, batch: b, now: time.Now().UTC(),
			byID: map[string]repo.MemoryRow{}, byTopic: map[topicKey]string{}, blocked: forgottenSources(current),
		}
		for _, e := range current {
			s.byID[e.ID] = e
			s.byTopic[topicKey{e.Scope, e.Topic}] = e.ID
		}
		if err := s.apply(ops); err != nil {
			return err
		}
		return tx.AdvanceMemory(b.Cursor, s.now, report)
	})
}

type applyState struct {
	tx      *repo.DB
	batch   *Batch
	now     time.Time
	byID    map[string]repo.MemoryRow
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
			e.Status = repo.MemoryRetired
		} else {
			e.Text, e.Kind, e.Status = op.Text, op.Kind, repo.MemoryActive
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

// topicKey finds a memory by topic: topics are unique within a scope, and
// a batch sees two scopes, global and its own.
type topicKey struct {
	scope repo.MemoryScope
	topic string
}

func (s *applyState) target(op Operation) (repo.MemoryRow, bool, error) {
	var e repo.MemoryRow
	if op.Action == "add" {
		added := repo.MemoryRow{ID: uuid.NewString(), Scope: s.batch.Scope, WorkspaceID: s.batch.WorkspaceID, Topic: op.Topic, CreatedAt: s.now}
		if op.Scope == scopeGlobal {
			added.Scope, added.WorkspaceID, added.OriginWorkspaceID = repo.MemoryGlobal, "", s.batch.WorkspaceID
		}
		id, exists := s.byTopic[topicKey{added.Scope, op.Topic}]
		if !exists {
			return added, true, nil
		}
		e = s.byID[id]
	} else {
		var ok bool
		if e, ok = s.byID[op.ID]; !ok {
			return repo.MemoryRow{}, false, fmt.Errorf("memory %s disappeared while the batch was being extracted", op.ID)
		}
	}
	if e.Status == repo.MemoryForgotten || e.UserEdited {
		return repo.MemoryRow{}, false, nil
	}
	return e, true, nil
}

func (s *applyState) blockedBy(sources []repo.MemorySource) bool {
	for _, src := range sources {
		if s.blocked[sourceKey(src)] {
			return true
		}
	}
	return false
}

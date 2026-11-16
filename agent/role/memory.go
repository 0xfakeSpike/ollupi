package role

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

// loadMemory records the memory snapshot the role recalls now, if any.
func (rt *Runtime) loadMemory() error {
	it, err := rt.recall()
	if err != nil {
		return fmt.Errorf("role: load memory: %w", err)
	}
	if it == nil {
		return nil
	}
	_, err = rt.record(*it)
	return err
}

// recall is the memory snapshot the run takes in now, as an item to record: none when the role takes in no memory,
// the user has none, or the conversation took one in since its latest compaction.
func (rt *Runtime) recall() (*db.ItemRow, error) {
	if !rt.role.recalls || rt.memory == nil || rt.hasSnapshot() {
		return nil, nil
	}
	snapshot, err := rt.memory.Snapshot(rt.thread.UserID, string(rt.thread.WorkspaceID), rt.window.MemoryBudget())
	if err != nil || snapshot == nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemMemoryContext, Status: db.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	}, nil
}

// hasSnapshot says whether the conversation took in a memory snapshot since its latest compaction.
func (rt *Runtime) hasSnapshot() bool {
	items := rt.conversation()
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case it.Type == db.ItemMemoryContext && it.Status == db.ItemCompleted:
			return true
		case it.Type == db.ItemContextCompaction && it.Status == db.ItemCompleted:
			return false
		}
	}
	return false
}

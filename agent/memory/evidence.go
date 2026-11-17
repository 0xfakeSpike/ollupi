package memory

import (
	"encoding/json"
	"errors"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

var (
	evidenceTypes    = []db.ItemType{db.ItemUserMessage, db.ItemAgentMessage, db.ItemToolCall, db.ItemError}
	evidenceStatuses = []db.ItemStatus{db.ItemCompleted, db.ItemFailed, db.ItemDeclined}
)

func nextBatch(store *db.DB, user string, limit int) (*Batch, error) {
	var batch *Batch
	err := store.Tx(func(tx *db.DB) error {
		state, err := tx.MemoryState(user)
		if err != nil {
			return err
		}
		c := state.Cursor
		var turn db.TurnRow
		var items []db.ItemRow
		if c.ID != "" {
			switch turn, err = tx.EndedTurn(c.ID); {
			case errors.Is(err, db.ErrNotFound):
			case err != nil:
				return err
			default:
				if items, err = tx.TurnItemsAfter(turn.ID, c.Seq, evidenceTypes, evidenceStatuses, limit); err != nil {
					return err
				}
			}
		}
		if len(items) == 0 {
			if turn, err = tx.NextEndedTurn(user, c); errors.Is(err, db.ErrNotFound) {
				return nil
			} else if err != nil {
				return err
			}
			if items, err = tx.TurnItemsAfter(turn.ID, 0, evidenceTypes, evidenceStatuses, limit); err != nil {
				return err
			}
		}
		thread, err := tx.ThreadRow(turn.ThreadID)
		if err != nil {
			return err
		}
		b := &Batch{
			UserID: user, Scope: localScope(string(thread.WorkspaceID)), WorkspaceID: string(thread.WorkspaceID), ThreadID: turn.ThreadID, TurnID: turn.ID,
			TurnStatus: turn.Status, TurnError: clipMemory(turn.Error, 1000), Facts: []Fact{},
			Cursor: db.MemoryCursor{At: turn.CompletedAt, ID: turn.ID}, Entries: len(items),
		}
		if len(items) > 0 {
			b.Cursor.Seq = items[len(items)-1].Seq
		}
		if b.Existing, err = tx.Memories(user, b.Scope, b.WorkspaceID, true); err != nil {
			return err
		}
		blocked := forgottenSources(b.Existing)
		for _, it := range items {
			if blocked[sourceKey(db.MemorySource{Table: "items", ID: it.ID})] {
				continue
			}
			b.Facts = append(b.Facts, itemFact(it))
		}
		batch = b
		return nil
	})
	return batch, err
}

func localScope(workspaceID string) db.MemoryScope {
	if workspaceID == "" {
		return db.MemoryGeneral
	}
	return db.MemoryWorkspace
}

func forgottenSources(entries []db.MemoryRow) map[string]bool {
	blocked := map[string]bool{}
	for _, e := range entries {
		if e.Status != db.MemoryForgotten {
			continue
		}
		for _, s := range e.Sources {
			blocked[sourceKey(s)] = true
		}
	}
	return blocked
}

const factRunes = 4000

type evidenceField struct {
	key, value string
	bound      int
}

func itemFact(it db.ItemRow) Fact {
	textBound := 3000
	if it.Type == db.ItemUserMessage {
		textBound = 1500
	}
	var result string
	if it.Result != nil {
		result = string(it.Result.Output)
	}
	fields := []evidenceField{
		{"text", it.Text, textBound}, {"tool", it.Tool, 1500}, {"arguments", it.Arguments, 1500},
		{"result", result, 3000}, {"error", it.Error, 1500},
	}
	data := map[string]any{"type": it.Type, "status": it.Status}
	remaining := factRunes
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if remaining < 100 {
			data[f.key] = "[omitted: evidence budget reached]"
			continue
		}
		text := clipMemory(f.value, min(f.bound, remaining))
		data[f.key] = text
		remaining -= len([]rune(text))
	}
	raw, _ := json.Marshal(data) // strings alone: it cannot fail
	return Fact{Source: db.MemorySource{Table: "items", ID: it.ID, OccurredAt: it.StartedAt.UTC().Format(time.RFC3339Nano)}, Data: raw}
}

// clipMemory keeps the first n runes of s, or fewer, so as not to cut a sensitive value in two: the part kept would
// no longer be masked on its way to the model.
func clipMemory(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	n, _ = redact.Cuts(s, redact.Spans(s), n, n)
	return string(r[:n]) + "\n[truncated]"
}

package memory

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
)

var (
	evidenceTypes    = []repo.ItemType{repo.ItemUserMessage, repo.ItemAgentMessage, repo.ItemToolCall, repo.ItemError}
	evidenceStatuses = []repo.ItemStatus{repo.ItemCompleted, repo.ItemFailed, repo.ItemDeclined}
)

func nextBatch(db *repo.DB, limit int) (*Batch, error) {
	var batch *Batch
	err := db.Tx(func(tx *repo.DB) error {
		state, err := tx.MemoryState()
		if err != nil {
			return err
		}
		c := state.Cursor
		var turn repo.TurnRow
		var items []repo.ItemRow
		if c.ID != "" {
			if turn, err = tx.EndedTurn(c.ID); err != nil {
				return err
			}
			if items, err = tx.TurnItemsAfter(turn.ID, c.Seq, evidenceTypes, evidenceStatuses, limit); err != nil {
				return err
			}
		}
		if len(items) == 0 {
			if turn, err = tx.NextEndedTurn(c); repo.IsNotFound(err) {
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
			Scope: localScope(string(thread.WorkspaceID)), WorkspaceID: string(thread.WorkspaceID), ThreadID: turn.ThreadID, TurnID: turn.ID,
			TurnStatus: turn.Status, TurnError: clipMemory(turn.Error, 1000), Facts: []Fact{},
			Cursor: repo.MemoryCursor{At: turn.CompletedAt, ID: turn.ID}, Entries: len(items),
		}
		if len(items) > 0 {
			b.Cursor.Seq = items[len(items)-1].Seq
		}
		if b.Existing, err = tx.Memories(b.Scope, b.WorkspaceID, true); err != nil {
			return err
		}
		blocked := forgottenSources(b.Existing)
		for _, it := range items {
			if blocked["items/"+it.ID] {
				continue
			}
			f, err := itemFact(it)
			if err != nil {
				return err
			}
			b.Facts = append(b.Facts, f)
		}
		batch = b
		return nil
	})
	return batch, err
}

// localScope is the scope of a thread's own memories: its workspace's, or
// general outside any workspace.
func localScope(workspaceID string) repo.MemoryScope {
	if workspaceID == "" {
		return repo.MemoryGeneral
	}
	return repo.MemoryWorkspace
}

func forgottenSources(entries []repo.MemoryRow) map[string]bool {
	blocked := map[string]bool{}
	for _, e := range entries {
		if e.Status != repo.MemoryForgotten {
			continue
		}
		for _, s := range e.Sources {
			blocked[s.Table+"/"+s.ID] = true
		}
	}
	return blocked
}

const factRunes = 4000

func itemFact(it repo.ItemRow) (Fact, error) {
	textBound := 3000
	if it.Type == repo.ItemUserMessage {
		textBound = 1500
	}
	fields := []struct {
		key, value string
		bound      int
	}{
		{"text", it.Text, textBound}, {"tool", it.Tool, 1500}, {"arguments", it.Arguments, 1500},
		{"result", resultText(it), 3000}, {"error", it.Error, 1500},
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
		remaining -= runeLen(text)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Fact{}, err
	}
	return Fact{Source: repo.MemorySource{Table: "items", ID: it.ID, OccurredAt: it.StartedAt.UTC().Format(time.RFC3339Nano)}, Data: raw}, nil
}

func resultText(it repo.ItemRow) string {
	if it.Result == nil {
		return ""
	}
	if it.Error != "" {
		return clipMemory(it.Error, 1000)
	}
	return string(it.Result.Output)
}

func clipMemory(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n[truncated]"
}

func runeLen(s string) int { return len([]rune(s)) }

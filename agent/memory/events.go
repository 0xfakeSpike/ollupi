package memory

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

const settleDelay = 5 * time.Second

var (
	evidenceTypes    = []repo.ItemType{repo.ItemUserMessage, repo.ItemAgentMessage, repo.ItemToolCall, repo.ItemError}
	evidenceStatuses = []repo.ItemStatus{repo.ItemCompleted, repo.ItemFailed, repo.ItemDeclined}
)

type entry struct {
	at     time.Time
	scope  string
	facts  []Fact
	cursor repo.MemoryCursor
}

func nextBatch(db *repo.DB, limit int) (*Batch, error) {
	if limit < 1 {
		limit = 16
	}
	if limit > 100 {
		limit = 100
	}
	var batch *Batch
	err := db.Tx(func(tx *repo.DB) error {
		state, err := tx.MemoryState()
		if err != nil {
			return err
		}
		before := time.Now().Add(-settleDelay)
		threads := map[string]repo.ThreadRow{}
		turns, err := turnEntries(tx, state.Cursors.Turns, before, limit, threads)
		if err != nil {
			return err
		}
		workspaces, err := workspaceEntries(tx, state.Cursors.Workspaces, before, limit)
		if err != nil {
			return err
		}
		jobs, err := cronEntries(tx, state.Cursors.CronJobs, before, limit, threads)
		if err != nil {
			return err
		}
		b := &Batch{Cursors: state.Cursors}
		streams := [][]entry{turns, workspaces, jobs}
		cursors := []*repo.MemoryCursor{&b.Cursors.Turns, &b.Cursors.Workspaces, &b.Cursors.CronJobs}
		pos := make([]int, len(streams))
		stopped := make([]bool, len(streams))
		seen := map[string]bool{}
		for b.Entries < limit {
			k := -1
			for i, s := range streams {
				if stopped[i] || pos[i] >= len(s) {
					continue
				}
				if k < 0 || s[pos[i]].at.Before(streams[k][pos[k]].at) {
					k = i
				}
			}
			if k < 0 {
				break
			}
			e := streams[k][pos[k]]
			if b.Entries == 0 {
				b.WorkspaceID = e.scope
			} else if e.scope != b.WorkspaceID {
				stopped[k] = true
				continue
			}
			for _, f := range e.facts {
				if key := f.Source.Table + "/" + f.Source.ID; !seen[key] {
					seen[key] = true
					b.Facts = append(b.Facts, f)
				}
			}
			*cursors[k] = e.cursor
			pos[k]++
			b.Entries++
		}
		if b.Entries == 0 {
			return nil
		}
		if b.Existing, err = tx.Memories(b.WorkspaceID, false); err != nil {
			return err
		}
		batch = b
		return nil
	})
	return batch, err
}

func threadOf(tx *repo.DB, id string, threads map[string]repo.ThreadRow) (repo.ThreadRow, error) {
	if t, ok := threads[id]; ok {
		return t, nil
	}
	t, err := tx.ThreadRow(id)
	if err == nil {
		threads[id] = t
	}
	return t, err
}

func turnEntries(tx *repo.DB, c repo.MemoryCursor, before time.Time, limit int, threads map[string]repo.ThreadRow) ([]entry, error) {
	var out []entry
	addItems := func(turn repo.TurnRow, scope string, at time.Time, after int) error {
		if len(out) >= limit {
			return nil
		}
		items, err := tx.TurnItemsAfter(turn.ID, after, evidenceTypes, evidenceStatuses, limit-len(out))
		if err != nil {
			return err
		}
		for _, it := range items {
			fact, err := itemFact(it)
			if err != nil {
				return err
			}
			out = append(out, entry{at: at, scope: scope, facts: []Fact{fact}, cursor: repo.MemoryCursor{At: &at, ID: turn.ID, Seq: it.Seq}})
		}
		return nil
	}
	if c.ID != "" && c.At != nil {
		turn, err := tx.EndedTurn(c.ID)
		if err != nil && !repo.IsNotFound(err) {
			return nil, err
		}
		if err == nil {
			thread, err := threadOf(tx, turn.ThreadID, threads)
			if err != nil {
				return nil, err
			}
			if err := addItems(turn, string(thread.WorkspaceID), c.At.UTC(), c.Seq); err != nil {
				return nil, err
			}
		}
	}
	if len(out) >= limit {
		return out, nil
	}
	turns, err := tx.EndedTurnsAfter(c, before, limit-len(out))
	if err != nil {
		return nil, err
	}
	for _, turn := range turns {
		if len(out) >= limit {
			break
		}
		thread, err := threadOf(tx, turn.ThreadID, threads)
		if err != nil {
			return nil, err
		}
		tf, err := threadFact(thread)
		if err != nil {
			return nil, err
		}
		uf, err := turnFact(turn)
		if err != nil {
			return nil, err
		}
		at := turn.CompletedAt.UTC()
		scope := string(thread.WorkspaceID)
		out = append(out, entry{at: at, scope: scope, facts: []Fact{tf, uf}, cursor: repo.MemoryCursor{At: &at, ID: turn.ID}})
		if err := addItems(turn, scope, at, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func workspaceEntries(tx *repo.DB, c repo.MemoryCursor, before time.Time, limit int) ([]entry, error) {
	rows, err := tx.WorkspacesChangedAfter(c, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(rows))
	for _, w := range rows {
		f, err := fact("workspaces", w.ID, w.UpdatedAt, map[string]any{"name": clipMemory(w.Name, 500), "rootPath": clipMemory(w.RootPath, 1000), "archived": w.ArchivedAt != nil})
		if err != nil {
			return nil, err
		}
		at := w.UpdatedAt.UTC()
		out = append(out, entry{at: at, scope: w.ID, facts: []Fact{f}, cursor: repo.MemoryCursor{At: &at, ID: w.ID}})
	}
	return out, nil
}

func cronEntries(tx *repo.DB, c repo.MemoryCursor, before time.Time, limit int, threads map[string]repo.ThreadRow) ([]entry, error) {
	rows, err := tx.CronJobsCreatedAfter(c, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(rows))
	for _, j := range rows {
		thread, err := threadOf(tx, j.ThreadID, threads)
		if err != nil {
			return nil, err
		}
		scope := string(thread.WorkspaceID)
		f, err := fact("cron_jobs", j.ID, j.CreatedAt, map[string]any{"workspaceId": scope, "threadId": j.ThreadID, "schedule": clipMemory(j.Spec, 250), "prompt": clipMemory(j.Prompt, 3000)})
		if err != nil {
			return nil, err
		}
		at := j.CreatedAt.UTC()
		out = append(out, entry{at: at, scope: scope, facts: []Fact{f}, cursor: repo.MemoryCursor{At: &at, ID: j.ID}})
	}
	return out, nil
}

func threadFact(t repo.ThreadRow) (Fact, error) {
	return fact("threads", t.ID, t.UpdatedAt, map[string]any{"workspaceId": t.WorkspaceID, "parentThreadId": t.ParentThreadID})
}

func turnFact(t repo.TurnRow) (Fact, error) {
	return fact("turns", t.ID, t.StartedAt, map[string]any{"threadId": t.ThreadID, "status": t.Status, "error": clipMemory(t.Error, 1000)})
}

func fact(table, id string, at time.Time, data map[string]any) (Fact, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Fact{}, err
	}
	return Fact{Source: repo.MemorySource{Table: table, ID: id, Operation: "upsert", OccurredAt: stamp(at)}, Data: raw}, nil
}

func itemFact(it repo.ItemRow) (Fact, error) {
	data := map[string]any{"threadId": it.ThreadID, "turnId": it.TurnID, "type": it.Type, "status": it.Status}
	remaining := 4000
	for key, value := range map[string]any{
		"text": it.Text, "tool": it.Tool, "arguments": it.Arguments, "result": resultText(it), "error": it.Error,
	} {
		if value == "" || value == nil {
			continue
		}
		if remaining < 100 {
			data[key] = "[omitted: evidence budget reached]"
			continue
		}
		bound := 1500
		if key == "text" {
			bound = 3000
			if it.Type == repo.ItemUserMessage {
				bound = 1500
			}
		}
		if key == "result" {
			bound = 3000
		}
		if bound > remaining {
			bound = remaining
		}
		text := value.(string)
		if runeLen(text) > bound {
			text = clipMemory(text, bound)
		}
		data[key] = text
		remaining -= runeLen(text)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Fact{}, err
	}
	return Fact{Source: repo.MemorySource{Table: "items", ID: it.ID, Operation: "upsert", OccurredAt: stamp(it.StartedAt)}, Data: raw}, nil
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

func visibleEvidence(batch *Batch) *Batch {
	blocked := map[string]bool{}
	for _, e := range batch.Existing {
		if e.Status != "forgotten" {
			continue
		}
		for _, s := range e.Sources {
			blocked[s.Table+"/"+s.ID] = true
		}
	}
	out := *batch
	out.Facts = make([]Fact, 0, len(batch.Facts))
	for _, f := range batch.Facts {
		if !blocked[f.Source.Table+"/"+f.Source.ID] {
			out.Facts = append(out.Facts, f)
		}
	}
	return &out
}

func clipMemory(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n[truncated]"
}

func runeLen(s string) int { return len([]rune(s)) }

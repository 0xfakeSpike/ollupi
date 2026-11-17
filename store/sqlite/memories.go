package sqlite

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/store"
)

type memoryRow struct {
	ID                string
	UserID            string
	Scope             string
	WorkspaceID       string
	OriginWorkspaceID string
	Topic             string
	Kind              string
	Text              string
	Status            string
	UserEdited        bool
	Sources           string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (row memoryRow) memory() (memory.Memory, error) {
	m := memory.Memory{ID: row.ID, User: row.UserID, Scope: memory.Scope(row.Scope), Workspace: row.WorkspaceID, Origin: row.OriginWorkspaceID,
		Topic: row.Topic, Kind: memory.Kind(row.Kind), Text: row.Text, Status: memory.Status(row.Status), UserEdited: row.UserEdited,
		Created: row.CreatedAt, Updated: row.UpdatedAt}
	if err := json.Unmarshal([]byte(row.Sources), &m.Sources); err != nil {
		return memory.Memory{}, fmt.Errorf("store: the sources of memory %s: %w", row.ID, err)
	}
	return m, nil
}

func (r reader) Memories(owner string, places ...memory.Place) ([]memory.Memory, error) {
	q := r.g.Table("memories").Where("user_id = ?", owner)
	if len(places) > 0 {
		at := r.g.Where("1 = 0")
		for _, p := range places {
			at = at.Or("scope = ? AND workspace_id = ?", string(p.Scope), p.Workspace)
		}
		q = q.Where(at)
	}
	var rows []memoryRow
	if err := q.Order("updated_at DESC, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: the memories of user %s: %w", owner, err)
	}
	out := make([]memory.Memory, 0, len(rows))
	for _, row := range rows {
		m, err := row.memory()
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (r reader) Memory(owner, id string) (memory.Memory, error) {
	var row memoryRow
	if err := notFound(r.g.Raw("SELECT * FROM memories WHERE id = ? AND user_id = ?", id, owner).Take(&row).Error); err != nil {
		return memory.Memory{}, fmt.Errorf("store: memory %s: %w", id, err)
	}
	return row.memory()
}

func (r reader) MemoryState(owner string) (memory.State, error) {
	var rows []struct {
		CursorSeq    int64
		CursorThread string
		CursorTurn   string
		State        string
	}
	if err := r.g.Raw("SELECT cursor_seq, cursor_thread, cursor_turn, state FROM memory_state WHERE user_id = ?", owner).Scan(&rows).Error; err != nil {
		return memory.State{}, fmt.Errorf("store: the memory state of user %s: %w", owner, err)
	}
	var s memory.State
	if len(rows) == 0 {
		return s, nil
	}
	if err := json.Unmarshal([]byte(rows[0].State), &s); err != nil {
		return memory.State{}, fmt.Errorf("store: the memory state of user %s: %w", owner, err)
	}
	s.Cursor = memory.Cursor{Seq: rows[0].CursorSeq, Thread: rows[0].CursorThread, Turn: rows[0].CursorTurn}
	return s, nil
}

func (r reader) Ended(owner string, after memory.Cursor, limit int) ([]memory.Cursor, error) {
	var rows []struct {
		EndedSeq int64
		ThreadID string
		ID       string
	}
	err := r.g.Raw(`SELECT t.ended_seq, t.thread_id, t.id FROM turns t JOIN threads th ON th.id = t.thread_id
		WHERE t.ended_seq IS NOT NULL AND (t.ended_seq, t.thread_id, t.id) > (?, ?, ?) AND th.user_id = ?
		ORDER BY t.ended_seq, t.thread_id, t.id LIMIT ?`, after.Seq, after.Thread, after.Turn, owner, limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: the ended turns of user %s: %w", owner, err)
	}
	out := make([]memory.Cursor, len(rows))
	for i, row := range rows {
		out[i] = memory.Cursor{Seq: row.EndedSeq, Thread: row.ThreadID, Turn: row.ID}
	}
	return out, nil
}

func (r reader) Unread(owner string, after memory.Cursor) (int64, error) {
	var n int64
	err := r.g.Raw(`SELECT count(*) FROM turns t JOIN threads th ON th.id = t.thread_id
		WHERE t.ended_seq IS NOT NULL AND (t.ended_seq, t.thread_id, t.id) > (?, ?, ?) AND th.user_id = ?`,
		after.Seq, after.Thread, after.Turn, owner).Scan(&n).Error
	if err != nil {
		return 0, fmt.Errorf("store: the unread turns of user %s: %w", owner, err)
	}
	return n, nil
}

func (t *tx) PutMemory(m memory.Memory) error {
	sources, _ := json.Marshal(nonNil(m.Sources)) // plain data: it cannot fail
	err := t.g.Exec(`INSERT INTO memories(id, user_id, scope, workspace_id, origin_workspace_id, topic, kind, text, status, user_edited, sources, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET scope = excluded.scope, workspace_id = excluded.workspace_id, origin_workspace_id = excluded.origin_workspace_id,
			topic = excluded.topic, kind = excluded.kind, text = excluded.text, status = excluded.status, user_edited = excluded.user_edited,
			sources = excluded.sources, updated_at = excluded.updated_at`,
		m.ID, m.User, string(m.Scope), m.Workspace, m.Origin, m.Topic, string(m.Kind), m.Text, string(m.Status), m.UserEdited, string(sources),
		m.Created.UTC(), m.Updated.UTC()).Error
	if err != nil {
		return fmt.Errorf("store: memory %s: %w", m.ID, err)
	}
	t.owned = append(t.owned, store.Event{User: m.User, Owned: &store.Owned{Memory: &m}})
	return nil
}

func (t *tx) SetMemoryState(owner string, s memory.State) error {
	state, _ := json.Marshal(s) // plain data: it cannot fail
	err := t.g.Exec(`INSERT INTO memory_state(user_id, cursor_seq, cursor_thread, cursor_turn, state) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET cursor_seq = excluded.cursor_seq, cursor_thread = excluded.cursor_thread,
			cursor_turn = excluded.cursor_turn, state = excluded.state`,
		owner, s.Cursor.Seq, s.Cursor.Thread, s.Cursor.Turn, string(state)).Error
	if err != nil {
		return fmt.Errorf("store: the memory state of user %s: %w", owner, err)
	}
	return nil
}

package sqlite

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	"gorm.io/gorm"
)

type reader struct {
	g *gorm.DB
}

type threadRow struct {
	ID            string
	UserID        string
	Title         string
	Settings      string
	WorkspaceID   string
	EnvironmentID string
	ParentID      *string
	ParentItem    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
	RepliedAt     *time.Time
	ReadAt        *time.Time
	Head          int64
	Status        string
	RunnableSince *time.Time
}

func (r threadRow) thread() (store.Thread, error) {
	var settings policy.Settings
	if err := json.Unmarshal([]byte(r.Settings), &settings); err != nil {
		return store.Thread{}, fmt.Errorf("store: the settings of thread %s: %w", r.ID, err)
	}
	t := store.Thread{
		Thread: user.Thread{ID: r.ID, Title: r.Title, Settings: settings, Workspace: r.WorkspaceID, Environment: r.EnvironmentID,
			ParentItem: r.ParentItem, Created: r.CreatedAt, Updated: r.UpdatedAt, Archived: r.ArchivedAt, Replied: r.RepliedAt, Read: r.ReadAt},
		User: r.UserID, Head: r.Head, Status: conversation.ThreadStatus(r.Status), Runnable: r.RunnableSince != nil,
	}
	if r.ParentID != nil {
		t.Parent = *r.ParentID
	}
	return t, nil
}

func (r reader) Thread(id string) (store.Thread, error) {
	var row threadRow
	if err := notFound(r.g.Raw("SELECT * FROM threads WHERE id = ?", id).Take(&row).Error); err != nil {
		return store.Thread{}, fmt.Errorf("store: thread %s: %w", id, err)
	}
	return row.thread()
}

type bodyRow struct {
	Body string
}

func (r reader) View(thread string) (conversation.View, error) {
	var head []int64
	if err := r.g.Raw("SELECT head FROM threads WHERE id = ?", thread).Scan(&head).Error; err != nil {
		return conversation.View{}, err
	}
	if len(head) == 0 {
		return conversation.View{}, fmt.Errorf("store: thread %s: %w", thread, store.ErrNotFound)
	}
	v := conversation.View{Head: head[0]}
	var err error
	if v.Turns, err = decodeRows[conversation.Turn](r.g, "SELECT body FROM turns WHERE thread_id = ? ORDER BY ord", thread); err != nil {
		return conversation.View{}, err
	}
	if v.Items, err = decodeRows[conversation.Item](r.g, "SELECT body FROM items WHERE thread_id = ? ORDER BY seq", thread); err != nil {
		return conversation.View{}, err
	}
	if v.Responses, err = decodeRows[conversation.Response](r.g, "SELECT body FROM responses WHERE thread_id = ? ORDER BY seq", thread); err != nil {
		return conversation.View{}, err
	}
	return v, nil
}

func decodeRows[T any](g *gorm.DB, query string, args ...any) ([]T, error) {
	var rows []bodyRow
	if err := g.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]T, len(rows))
	for i, row := range rows {
		if err := json.Unmarshal([]byte(row.Body), &out[i]); err != nil {
			return nil, fmt.Errorf("store: decode a %T: %w", out[i], err)
		}
	}
	return out, nil
}

func (r reader) Page(thread string, before int64, limit int) (store.Page, bool, error) {
	query := "SELECT body FROM items WHERE thread_id = ?"
	args := []any{thread}
	if before > 0 {
		query += " AND seq < ?"
		args = append(args, before)
	}
	items, err := decodeRows[conversation.Item](r.g, query+" ORDER BY seq DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return store.Page{}, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	slices.Reverse(items)
	var turns []string
	for _, it := range items {
		if !slices.Contains(turns, it.Turn) {
			turns = append(turns, it.Turn)
		}
	}
	page := store.Page{Items: items}
	if page.Turns, err = decodeRows[conversation.Turn](r.g, "SELECT body FROM turns WHERE thread_id = ? AND (id IN ? OR ended = 0) ORDER BY ord", thread, turns); err != nil {
		return store.Page{}, false, err
	}
	return page, more, nil
}

func (r reader) Runnable(limit int, except []string) ([]store.Runnable, error) {
	query := "SELECT id AS thread, user_id AS user FROM threads WHERE runnable_since IS NOT NULL"
	args := []any{}
	if len(except) > 0 {
		query += " AND user_id NOT IN ?"
		args = append(args, except)
	}
	var out []store.Runnable
	err := r.g.Raw(query+" ORDER BY parent_id IS NOT NULL, runnable_since, id LIMIT ?", append(args, limit)...).Scan(&out).Error
	return out, err
}

func (r reader) Seq() (int64, error) {
	var seq int64
	err := r.g.Raw("SELECT COALESCE(MAX(id), 0) FROM entries").Scan(&seq).Error
	return seq, err
}

type tx struct {
	reader
	views   map[string]conversation.View
	pending map[string]*pending
}

// pending is what the transaction did to one thread, for the feed.
type pending struct {
	user    string
	seq     int64
	change  store.Change
	items   []string
	turns   []string
	written bool
}

func (t *tx) note(thread, owner string) *pending {
	p := t.pending[thread]
	if p == nil {
		p = &pending{user: owner}
		t.pending[thread] = p
	}
	return p
}

func (t *tx) EnsureUser(id string) error {
	return t.g.Exec("INSERT INTO users(id, created_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING", id, time.Now().UTC()).Error
}

func (t *tx) CreateThread(th store.Thread) error {
	settings, _ := json.Marshal(th.Settings) // plain data: it cannot fail
	var parent *string
	if th.Parent != "" {
		parent = &th.Parent
	}
	err := t.g.Exec(`INSERT INTO threads(id, user_id, title, settings, workspace_id, environment_id, parent_id, parent_item, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, th.ID, th.User, th.Title, string(settings), th.Workspace, th.Environment, parent, th.ParentItem, th.Created, th.Updated).Error
	if err != nil {
		return fmt.Errorf("store: create thread %s: %w", th.ID, err)
	}
	t.note(th.ID, th.User).written = true
	return nil
}

func (t *tx) UpdateThread(th store.Thread) error {
	settings, _ := json.Marshal(th.Settings) // plain data: it cannot fail
	res := t.g.Exec(`UPDATE threads SET title = ?, settings = ?, workspace_id = ?, updated_at = ?, archived_at = ?, read_at = ? WHERE id = ?`,
		th.Title, string(settings), th.Workspace, th.Updated, th.Archived, th.Read, th.ID)
	if res.Error != nil {
		return fmt.Errorf("store: update thread %s: %w", th.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("store: thread %s: %w", th.ID, store.ErrNotFound)
	}
	t.note(th.ID, th.User).written = true
	return nil
}

func (t *tx) View(thread string) (conversation.View, error) {
	if v, ok := t.views[thread]; ok {
		return v, nil
	}
	v, err := t.reader.View(thread)
	if err != nil {
		return conversation.View{}, err
	}
	if t.views == nil {
		t.views = map[string]conversation.View{}
	}
	t.views[thread] = v
	return v, nil
}

func (t *tx) Append(thread string, entries ...conversation.Entry) (conversation.View, error) {
	th, err := t.Thread(thread)
	if err != nil {
		return conversation.View{}, err
	}
	v, err := t.View(thread)
	if err != nil {
		return conversation.View{}, err
	}
	p := t.note(thread, th.User)
	now := time.Now().UTC()
	appended := make([]conversation.Entry, 0, len(entries))
	for _, e := range entries {
		e.Seq = v.Head + 1
		if e.At.IsZero() {
			e.At = now
		}
		if err := conversation.Check(v, e); err != nil {
			return conversation.View{}, fmt.Errorf("store: thread %s: %w", thread, err)
		}
		v.Apply(e)
		body, err := json.Marshal(e.Body)
		if err != nil {
			return conversation.View{}, fmt.Errorf("store: encode a %s entry: %w", conversation.KindOf(e.Body), err)
		}
		res := t.g.Exec("INSERT INTO entries(thread_id, seq, turn_id, item_id, at, kind, body) VALUES (?, ?, ?, ?, ?, ?, ?)",
			thread, e.Seq, e.Turn, e.Item, e.At, string(conversation.KindOf(e.Body)), string(body))
		if res.Error != nil {
			return conversation.View{}, fmt.Errorf("store: append to thread %s: %w", thread, res.Error)
		}
		var id int64
		if err := t.g.Raw("SELECT last_insert_rowid()").Scan(&id).Error; err != nil {
			return conversation.View{}, err
		}
		p.seq = id
		appended = append(appended, e)
		p.change.Entries = append(p.change.Entries, e)
		if e.Item != "" && !slices.Contains(p.items, e.Item) {
			p.items = append(p.items, e.Item)
		}
		if !slices.Contains(p.turns, e.Turn) {
			p.turns = append(p.turns, e.Turn)
		}
		if _, ended := e.Body.(conversation.TurnEnded); ended && th.Parent == "" {
			th.Replied = &e.At
		}
	}
	t.views[thread] = v
	if err := t.persist(thread, th, v, p, appended, now); err != nil {
		return conversation.View{}, err
	}
	return v, nil
}

// persist writes what the entries changed of the view, and the thread's folded columns.
func (t *tx) persist(thread string, th store.Thread, v conversation.View, p *pending, entries []conversation.Entry, now time.Time) error {
	for ord, turn := range v.Turns {
		if !slices.ContainsFunc(entries, func(e conversation.Entry) bool { return e.Turn == turn.ID }) {
			continue
		}
		body, _ := json.Marshal(turn) // plain data: it cannot fail
		if err := t.g.Exec(`INSERT INTO turns(thread_id, id, ord, ended, body) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(thread_id, id) DO UPDATE SET ended = excluded.ended, body = excluded.body`, thread, turn.ID, ord, turn.Ended != nil, string(body)).Error; err != nil {
			return err
		}
	}
	for _, it := range v.Items {
		if !slices.ContainsFunc(entries, func(e conversation.Entry) bool { return e.Item == it.ID }) {
			continue
		}
		body, _ := json.Marshal(it) // plain data: it cannot fail
		if err := t.g.Exec(`INSERT INTO items(thread_id, id, turn_id, seq, updated, kind, status, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(thread_id, id) DO UPDATE SET updated = excluded.updated, status = excluded.status, body = excluded.body`,
			thread, it.ID, it.Turn, it.Seq, it.Updated, string(it.Kind), string(it.Status), string(body)).Error; err != nil {
			return err
		}
	}
	for _, r := range v.Responses {
		if !slices.ContainsFunc(entries, func(e conversation.Entry) bool { return e.Seq == r.Seq }) {
			continue
		}
		body, _ := json.Marshal(r) // plain data: it cannot fail
		if err := t.g.Exec("INSERT INTO responses(thread_id, id, seq, body) VALUES (?, ?, ?, ?)", thread, r.ID, r.Seq, string(body)).Error; err != nil {
			return err
		}
	}
	runnable := v.Runnable()
	err := t.g.Exec(`UPDATE threads SET head = ?, status = ?, updated_at = ?, replied_at = ?,
		runnable_since = CASE WHEN ? THEN COALESCE(runnable_since, ?) ELSE NULL END WHERE id = ?`,
		v.Head, string(v.Status()), now, th.Replied, runnable, now, thread).Error
	if err != nil {
		return err
	}
	p.change.Status, p.change.Runnable, p.change.At = v.Status(), runnable, now
	p.change.Items, p.change.Turns = nil, nil
	for _, id := range p.items {
		it, _ := v.ItemOf(id)
		p.change.Items = append(p.change.Items, it)
	}
	for _, id := range p.turns {
		turn, _ := v.TurnOf(id)
		p.change.Turns = append(p.change.Turns, turn)
	}
	return nil
}

func (t *tx) events() []store.Event {
	var out []store.Event
	for thread, p := range t.pending {
		change := p.change
		if p.written {
			if th, err := t.Thread(thread); err == nil {
				change.Header = &th
				change.Status, change.Runnable = th.Status, th.Runnable
			}
		}
		out = append(out, store.Event{Seq: p.seq, User: p.user, Thread: thread, Change: &change})
	}
	return out
}

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
	WorkspaceID   *string
	EnvironmentID *string
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
		Thread: user.Thread{ID: r.ID, Title: r.Title, Settings: settings, Workspace: deref(r.WorkspaceID), Environment: deref(r.EnvironmentID),
			Parent: deref(r.ParentID), ParentItem: r.ParentItem, Created: r.CreatedAt, Updated: r.UpdatedAt, Archived: r.ArchivedAt, Replied: r.RepliedAt, Read: r.ReadAt},
		User: r.UserID, Head: r.Head, Status: conversation.ThreadStatus(r.Status), Runnable: r.RunnableSince != nil,
	}
	return t, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	if page.Turns, err = decodeRows[conversation.Turn](r.g, "SELECT body FROM turns WHERE thread_id = ? AND (id IN ? OR ended_seq IS NULL) ORDER BY ord", thread, turns); err != nil {
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
	err := r.g.Raw("SELECT seq FROM feed WHERE id = 1").Scan(&seq).Error
	return seq, err
}

type tx struct {
	reader
	views   map[string]conversation.View
	threads map[string]*pending
	order   []string
	owned   []store.Event
	seq     int64
}

func (t *tx) number() (int64, error) {
	if t.seq == 0 {
		if err := t.g.Raw("UPDATE feed SET seq = seq + 1 WHERE id = 1 RETURNING seq").Scan(&t.seq).Error; err != nil {
			return 0, fmt.Errorf("store: number the commit: %w", err)
		}
	}
	return t.seq, nil
}

type pending struct {
	user   string
	change store.Change
	items  []string
	turns  []string
}

func (t *tx) note(thread, owner string) *pending {
	p := t.threads[thread]
	if p == nil {
		p = &pending{user: owner}
		t.threads[thread] = p
		t.order = append(t.order, thread)
	}
	return p
}

func (t *tx) CreateThread(th store.Thread) error {
	settings, _ := json.Marshal(th.Settings)
	err := t.g.Exec(`INSERT INTO threads(id, user_id, title, settings, workspace_id, environment_id, parent_id, parent_item, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, th.ID, th.User, th.Title, string(settings), orNull(th.Workspace), orNull(th.Environment), orNull(th.Parent),
		th.ParentItem, th.Created, th.Updated).Error
	if err != nil {
		return fmt.Errorf("store: create thread %s: %w", th.ID, err)
	}
	t.note(th.ID, th.User)
	return nil
}

func (t *tx) UpdateThread(th store.Thread) error {
	settings, _ := json.Marshal(th.Settings)
	res := t.g.Exec(`UPDATE threads SET title = ?, settings = ?, workspace_id = ?, updated_at = ?, archived_at = ?, read_at = ? WHERE id = ?`,
		th.Title, string(settings), orNull(th.Workspace), th.Updated, th.Archived, th.Read, th.ID)
	if res.Error != nil {
		return fmt.Errorf("store: update thread %s: %w", th.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("store: thread %s: %w", th.ID, store.ErrNotFound)
	}
	t.note(th.ID, th.User)
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
	before, _ := v.Active()
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
		if err := t.g.Exec("INSERT INTO entries(thread_id, seq, turn_id, item_id, at, kind, body) VALUES (?, ?, ?, ?, ?, ?, ?)",
			thread, e.Seq, e.Turn, e.Item, e.At, string(conversation.KindOf(e.Body)), string(body)).Error; err != nil {
			return conversation.View{}, fmt.Errorf("store: append to thread %s: %w", thread, err)
		}
		appended = append(appended, e)
		p.change.Entries = append(p.change.Entries, e)
		for _, id := range touched(e) {
			if !slices.Contains(p.items, id) {
				p.items = append(p.items, id)
			}
		}
		if !slices.Contains(p.turns, e.Turn) {
			p.turns = append(p.turns, e.Turn)
		}
		if _, ended := e.Body.(conversation.TurnEnded); ended && th.Parent == "" {
			th.Replied = &e.At
		}
	}
	if after, _ := v.Active(); after.ID != before.ID && after.ID != "" && !slices.Contains(p.turns, after.ID) {
		p.turns = append(p.turns, after.ID)
	}
	t.views[thread] = v
	if err := t.persist(thread, th, v, p, appended, now); err != nil {
		return conversation.View{}, err
	}
	return v, nil
}

func touched(e conversation.Entry) []string {
	var ids []string
	if e.Item != "" {
		ids = append(ids, e.Item)
	}
	if r, ok := e.Body.(conversation.ChildReported); ok {
		ids = append(ids, r.Launch)
	}
	return ids
}

func (t *tx) persist(thread string, th store.Thread, v conversation.View, p *pending, entries []conversation.Entry, now time.Time) error {
	for ord, turn := range v.Turns {
		if !slices.ContainsFunc(entries, func(e conversation.Entry) bool { return e.Turn == turn.ID }) {
			continue
		}
		body, _ := json.Marshal(turn)
		var ended *int64
		if turn.Ended != nil {
			seq, err := t.number()
			if err != nil {
				return err
			}
			ended = &seq
		}
		if err := t.g.Exec(`INSERT INTO turns(thread_id, id, ord, ended_seq, cron_job, body) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(thread_id, id) DO UPDATE SET ended_seq = COALESCE(turns.ended_seq, excluded.ended_seq), body = excluded.body`,
			thread, turn.ID, ord, ended, turn.CronJob, string(body)).Error; err != nil {
			return err
		}
	}
	var changed []string
	for _, e := range entries {
		changed = append(changed, touched(e)...)
	}
	for _, it := range v.Items {
		if !slices.Contains(changed, it.ID) {
			continue
		}
		body, _ := json.Marshal(it)
		text := ""
		if it.Kind == conversation.ItemUserMessage || it.Kind == conversation.ItemAgentMessage {
			text = it.Text
		}
		if err := t.g.Exec(`INSERT INTO items(thread_id, id, turn_id, seq, updated, kind, status, at, text, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(thread_id, id) DO UPDATE SET updated = excluded.updated, status = excluded.status, body = excluded.body`,
			thread, it.ID, it.Turn, it.Seq, it.Updated, string(it.Kind), string(it.Status), it.StartedAt, text, string(body)).Error; err != nil {
			return err
		}
	}
	for _, r := range v.Responses {
		if !slices.ContainsFunc(entries, func(e conversation.Entry) bool { return e.Seq == r.Seq }) {
			continue
		}
		body, _ := json.Marshal(r)
		if err := t.g.Exec("INSERT INTO responses(thread_id, id, seq, body) VALUES (?, ?, ?, ?)", thread, r.ID, r.Seq, string(body)).Error; err != nil {
			return err
		}
	}
	runnable := v.Runnable()
	err := t.g.Exec(`UPDATE threads SET head = ?, own_status = ?, updated_at = ?, replied_at = ?,
		runnable_since = CASE WHEN ? THEN COALESCE(runnable_since, ?) ELSE NULL END WHERE id = ?`,
		v.Head, string(v.Status()), now, th.Replied, runnable, now, thread).Error
	if err != nil {
		return err
	}
	if err := t.rollUp(thread); err != nil {
		return err
	}
	active, _ := v.Active()
	p.change.Active, p.change.Runnable, p.change.At = active.ID, runnable, now
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

var waitsOnUser = []string{string(conversation.ThreadWaitingOnApproval), string(conversation.ThreadWaitingOnUserInput)}

func (t *tx) rollUp(id string) error {
	for id != "" {
		var row struct {
			UserID    string
			ParentID  *string
			Status    string
			OwnStatus string
		}
		if err := t.g.Raw("SELECT user_id, parent_id, status, own_status FROM threads WHERE id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		var subagents []conversation.ThreadStatus
		if err := t.g.Raw("SELECT status FROM threads WHERE parent_id = ? AND status IN ?", id, waitsOnUser).Scan(&subagents).Error; err != nil {
			return err
		}
		status := conversation.RollUp(conversation.ThreadStatus(row.OwnStatus), subagents)
		if string(status) == row.Status {
			return nil
		}
		if err := t.g.Exec("UPDATE threads SET status = ? WHERE id = ?", string(status), id).Error; err != nil {
			return err
		}
		t.note(id, row.UserID)
		id = deref(row.ParentID)
	}
	return nil
}

func (t *tx) events() []store.Event {
	var out []store.Event
	for _, thread := range t.order {
		p := t.threads[thread]
		change := p.change
		if !change.Deleted {
			if th, err := t.Thread(thread); err == nil {
				change.Header = &th
				change.Status, change.Runnable = th.Status, th.Runnable
			}
		}
		out = append(out, store.Event{User: p.user, Thread: thread, Change: &change})
	}
	return append(out, t.owned...)
}

package sqlite

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
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
		Thread: account.Thread{ID: r.ID, Title: r.Title, Settings: settings, Workspace: deref(r.WorkspaceID), Environment: deref(r.EnvironmentID),
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
	var v conversation.View
	if err := notFound(r.g.Raw("SELECT head FROM threads WHERE id = ?", thread).Take(&v.Head).Error); err != nil {
		return conversation.View{}, fmt.Errorf("store: thread %s: %w", thread, err)
	}
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
	return r.queue("SELECT id, user_id AS user, id AS thread FROM threads", []string{"runnable_since IS NOT NULL"},
		"parent_id IS NOT NULL, runnable_since, id", limit, except)
}

func (r reader) Tasks(limit int, except []string) ([]store.Runnable, error) {
	return r.queue("SELECT id, user_id AS user, thread_id AS thread FROM tasks", nil, "created_at, id", limit, except)
}

func (r reader) queue(query string, where []string, order string, limit int, except []string) ([]store.Runnable, error) {
	args := []any{}
	if len(except) > 0 {
		where = append(where, "user_id NOT IN ?")
		args = append(args, except)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	var out []store.Runnable
	err := r.g.Raw(query+" ORDER BY "+order+" LIMIT ?", append(args, limit)...).Scan(&out).Error
	return out, err
}

type taskRow struct {
	ID        string
	UserID    string
	ThreadID  string
	TurnID    string
	ItemID    string
	Call      string
	CreatedAt time.Time
	StartedAt *time.Time
}

func (r reader) Task(id string) (store.Task, error) {
	var row taskRow
	if err := notFound(r.g.Raw("SELECT * FROM tasks WHERE id = ?", id).Take(&row).Error); err != nil {
		return store.Task{}, fmt.Errorf("store: task %s: %w", id, err)
	}
	return store.Task{ID: row.ID, User: row.UserID, Thread: row.ThreadID, Turn: row.TurnID, Item: row.ItemID,
		Call: json.RawMessage(row.Call), Created: row.CreatedAt, Started: row.StartedAt}, nil
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

func (t *tx) commitSeq() (int64, error) {
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

func (t *tx) pendingFor(thread, owner string) *pending {
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
	t.pendingFor(th.ID, th.User)
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
	t.pendingFor(th.ID, th.User)
	return nil
}

func (t *tx) CreateTask(task store.Task) error {
	err := t.g.Exec("INSERT INTO tasks(id, user_id, thread_id, turn_id, item_id, call, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		task.ID, task.User, task.Thread, task.Turn, task.Item, string(task.Call), task.Created).Error
	if err != nil {
		return fmt.Errorf("store: create task %s: %w", task.ID, err)
	}
	return nil
}

func (t *tx) StartTask(id string, at time.Time) error {
	return t.taskExec("start", id, "UPDATE tasks SET started_at = ? WHERE id = ?", at, id)
}

func (t *tx) DeleteTask(id string) error {
	return t.taskExec("delete", id, "DELETE FROM tasks WHERE id = ?", id)
}

func (t *tx) taskExec(what, id, query string, args ...any) error {
	res := t.g.Exec(query, args...)
	if res.Error != nil {
		return fmt.Errorf("store: %s task %s: %w", what, id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("store: task %s: %w", id, store.ErrNotFound)
	}
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
	pend := t.pendingFor(thread, th.User)
	before, _ := v.Active()
	now := time.Now().UTC()
	wrote := written{turns: map[string]bool{}, items: map[string]bool{}, seqs: map[int64]bool{}}
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
		pend.change.Entries = append(pend.change.Entries, e)
		wrote.turns[e.Turn], wrote.seqs[e.Seq] = true, true
		for _, id := range touched(e) {
			wrote.items[id] = true
			if !slices.Contains(pend.items, id) {
				pend.items = append(pend.items, id)
			}
		}
		if !slices.Contains(pend.turns, e.Turn) {
			pend.turns = append(pend.turns, e.Turn)
		}
		if _, ended := e.Body.(conversation.TurnEnded); ended && th.Parent == "" {
			th.Replied = &e.At
		}
	}
	if after, _ := v.Active(); after.ID != before.ID && after.ID != "" && !slices.Contains(pend.turns, after.ID) {
		pend.turns = append(pend.turns, after.ID)
	}
	t.views[thread] = v
	if err := t.persist(thread, th, v, pend, wrote, now); err != nil {
		return conversation.View{}, err
	}
	return v, nil
}

func touched(e conversation.Entry) []string {
	var ids []string
	if e.Item != "" {
		ids = append(ids, e.Item)
	}
	switch r := e.Body.(type) {
	case conversation.ChildReported:
		ids = append(ids, r.Launch)
	case conversation.TaskReported:
		ids = append(ids, r.Launch)
	}
	return ids
}

type written struct {
	turns map[string]bool
	items map[string]bool
	seqs  map[int64]bool
}

func (t *tx) persist(thread string, th store.Thread, v conversation.View, p *pending, w written, now time.Time) error {
	for ord, turn := range v.Turns {
		if !w.turns[turn.ID] {
			continue
		}
		body, _ := json.Marshal(turn)
		var ended *int64
		if turn.Ended != nil {
			seq, err := t.commitSeq()
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
	for _, it := range v.Items {
		if !w.items[it.ID] {
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
		if !w.seqs[r.Seq] {
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
	p.change.Active = active.ID
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
		t.pendingFor(id, row.UserID)
		id = deref(row.ParentID)
	}
	return nil
}

func (t *tx) events() ([]store.Event, error) {
	var out []store.Event
	for _, thread := range t.order {
		p := t.threads[thread]
		change := p.change
		if !change.Deleted {
			th, err := t.Thread(thread)
			if err != nil {
				return nil, err
			}
			change.Header = &th
		}
		out = append(out, store.Event{User: p.user, Thread: thread, Change: &change})
	}
	return append(out, t.owned...), nil
}

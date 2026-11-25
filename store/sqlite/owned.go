package sqlite

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"gorm.io/gorm"
)

func (r reader) Users() ([]string, error) {
	var out []string
	err := r.g.Raw("SELECT id FROM users ORDER BY id").Scan(&out).Error
	return out, err
}

func (r reader) UserList() ([]account.User, error) {
	var out []account.User
	err := r.g.Raw("SELECT id, name FROM users ORDER BY id").Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("store: the users: %w", err)
	}
	return out, nil
}

func (r reader) User(id string) (account.User, error) {
	var u account.User
	if err := notFound(r.g.Raw("SELECT id, name FROM users WHERE id = ?", id).Take(&u).Error); err != nil {
		return account.User{}, fmt.Errorf("store: user %s: %w", id, err)
	}
	return u, nil
}

type settingsRow struct {
	Redaction    bool
	RedactionKey string
	Memory       bool
	Learn        bool
	Permissions  string
	Model        string
	LearnModel   string
}

func (r reader) Settings(owner string) (account.Settings, error) {
	var row settingsRow
	if err := notFound(r.g.Raw("SELECT * FROM settings WHERE user_id = ?", owner).Take(&row).Error); err != nil {
		return account.Settings{}, fmt.Errorf("store: the settings of user %s: %w", owner, err)
	}
	s := account.Settings{Redaction: row.Redaction, RedactionKey: row.RedactionKey, Memory: row.Memory, Learn: row.Learn, Model: row.Model, LearnModel: row.LearnModel}
	if err := json.Unmarshal([]byte(row.Permissions), &s.Permissions); err != nil {
		return account.Settings{}, fmt.Errorf("store: the permissions of user %s: %w", owner, err)
	}
	return s, nil
}

type workspaceRow struct {
	ID            string
	EnvironmentID string
	Name          string
	Root          string
	MovingTo      string
	Kind          string
	Instructions  string
	Providers     string
	Settings      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
}

func (row workspaceRow) workspace(base string) (account.Workspace, error) {
	w := account.Workspace{ID: row.ID, Name: row.Name, Environment: row.EnvironmentID, Root: readRoot(row.Root, base), MovingTo: readRoot(row.MovingTo, base), Kind: account.WorkspaceKind(row.Kind), Instructions: row.Instructions,
		Created: row.CreatedAt, Updated: row.UpdatedAt, Archived: row.ArchivedAt}
	if err := json.Unmarshal([]byte(row.Settings), &w.Settings); err != nil {
		return account.Workspace{}, fmt.Errorf("store: the settings of workspace %s: %w", row.ID, err)
	}
	if err := json.Unmarshal([]byte(row.Providers), &w.Providers); err != nil {
		return account.Workspace{}, fmt.Errorf("store: the providers of workspace %s: %w", row.ID, err)
	}
	return w, nil
}

func (r reader) Workspaces(owner string) ([]account.Workspace, error) {
	return each(r.g.Raw("SELECT * FROM workspaces WHERE user_id = ? ORDER BY name, id", owner),
		func(row workspaceRow) (account.Workspace, error) { return row.workspace(r.base) })
}

func (r reader) Moves() ([]store.Move, error) {
	var rows []struct{ UserID, ID string }
	if err := r.g.Raw("SELECT user_id, id FROM workspaces WHERE moving_to <> ''").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]store.Move, len(rows))
	for i, row := range rows {
		w, err := r.Workspace(row.UserID, row.ID)
		if err != nil {
			return nil, err
		}
		out[i] = store.Move{User: row.UserID, Workspace: w}
	}
	return out, nil
}

func (r reader) Workspace(owner, id string) (account.Workspace, error) {
	var row workspaceRow
	if err := notFound(r.g.Raw("SELECT * FROM workspaces WHERE id = ? AND user_id = ?", id, owner).Take(&row).Error); err != nil {
		return account.Workspace{}, fmt.Errorf("store: workspace %s: %w", id, err)
	}
	return row.workspace(r.base)
}

func (r reader) Scratch(owner, environment string) (account.Workspace, error) {
	var row workspaceRow
	err := r.g.Raw("SELECT * FROM workspaces WHERE user_id = ? AND environment_id = ? AND kind = ?", owner, environment, string(account.ScratchWorkspace)).Take(&row).Error
	if err := notFound(err); err != nil {
		return account.Workspace{}, fmt.Errorf("store: the scratch workspace of environment %s: %w", environment, err)
	}
	return row.workspace(r.base)
}

type environmentRow struct {
	ID        string
	Name      string
	Provider  string
	Config    string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (row environmentRow) environment() (account.Environment, error) {
	return account.Environment{ID: row.ID, Name: row.Name, Provider: row.Provider, Config: json.RawMessage(row.Config), State: json.RawMessage(row.State),
		Created: row.CreatedAt, Updated: row.UpdatedAt}, nil
}

// Environments are the user's, the one made with them first.
func (r reader) Environments(owner string) ([]account.Environment, error) {
	return each(r.g.Raw("SELECT * FROM environments WHERE user_id = ? ORDER BY created_at, id", owner), environmentRow.environment)
}

func (r reader) Environment(owner, id string) (account.Environment, error) {
	var row environmentRow
	if err := notFound(r.g.Raw("SELECT * FROM environments WHERE id = ? AND user_id = ?", id, owner).Take(&row).Error); err != nil {
		return account.Environment{}, fmt.Errorf("store: environment %s: %w", id, err)
	}
	return row.environment()
}

// EnvironmentInUse is whether a conversation or a workspace but its scratch
// one is in the environment.
func (r reader) EnvironmentInUse(owner, id string) (bool, error) {
	var n int64
	err := r.g.Raw(`SELECT (SELECT count(*) FROM threads t JOIN workspaces w ON w.id = t.workspace_id WHERE t.user_id = ? AND w.environment_id = ?)
		+ (SELECT count(*) FROM workspaces WHERE user_id = ? AND environment_id = ? AND kind <> ?)`, owner, id, owner, id, string(account.ScratchWorkspace)).Scan(&n).Error
	return n > 0, err
}

func (r reader) WorkspaceBusy(owner, id string) (bool, error) {
	var n int64
	err := r.g.Raw("SELECT count(*) FROM threads WHERE user_id = ? AND workspace_id = ? AND status <> ?", owner, id, string(conversation.ThreadIdle)).Scan(&n).Error
	return n > 0, err
}

type mcpRow struct {
	Name    string
	Command string
	Args    string
	Env     string
	URL     string
	Headers string
}

func (r reader) MCPServers(owner string) ([]account.MCPServer, error) {
	var rows []mcpRow
	if err := r.g.Raw("SELECT * FROM mcp_servers WHERE user_id = ? ORDER BY name", owner).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]account.MCPServer, len(rows))
	for i, row := range rows {
		s := account.MCPServer{Name: row.Name, Command: row.Command, URL: row.URL}
		if err := errors.Join(json.Unmarshal([]byte(row.Args), &s.Args), json.Unmarshal([]byte(row.Env), &s.Env), json.Unmarshal([]byte(row.Headers), &s.Headers)); err != nil {
			return nil, fmt.Errorf("store: MCP server %s: %w", row.Name, err)
		}
		out[i] = s
	}
	return out, nil
}

type cronRow struct {
	ID        string
	ThreadID  string
	Name      string
	Spec      string
	Prompt    string
	Times     *int
	Runs      int
	Paused    bool
	NextRunAt time.Time
	CreatedAt time.Time
}

func (row cronRow) job() (account.CronJob, error) {
	return account.CronJob{ID: row.ID, Thread: row.ThreadID, Name: row.Name, Spec: row.Spec, Prompt: row.Prompt, Times: row.Times,
		Runs: row.Runs, Paused: row.Paused, NextRun: row.NextRunAt, Created: row.CreatedAt}, nil
}

type cronJobViewRow struct {
	Job         cronRow `gorm:"embedded"`
	Title       string
	WorkspaceID *string
	ArchivedAt  *time.Time
	LastTurn    *string
}

const cronJobViewsFrom = `SELECT j.*, t.title, t.workspace_id, t.archived_at,
		(SELECT u.body FROM turns u WHERE u.cron_job = j.id ORDER BY u.ord DESC LIMIT 1) AS last_turn
	FROM cron_jobs j JOIN threads t ON t.id = j.thread_id`

func (row cronJobViewRow) view() (store.CronJobView, error) {
	job, _ := row.Job.job()
	t := store.CronJobView{CronJob: job, Title: row.Title, Workspace: deref(row.WorkspaceID), Archived: row.ArchivedAt != nil}
	if row.LastTurn == nil {
		return t, nil
	}
	var last conversation.Turn
	if err := json.Unmarshal([]byte(*row.LastTurn), &last); err != nil {
		return store.CronJobView{}, fmt.Errorf("store: the last run of task %s: %w", job.ID, err)
	}
	t.LastTurn, t.LastRun = last.ID, &last.QueuedAt
	return t, nil
}

func cronJobViews(g *gorm.DB, query string, args ...any) ([]store.CronJobView, error) {
	return each(g.Raw(cronJobViewsFrom+" "+query, args...), cronJobViewRow.view)
}

func (r reader) CronJobs(owner, thread string) ([]store.CronJobView, error) {
	query := "WHERE t.user_id = ?"
	args := []any{owner}
	if thread != "" {
		query += " AND j.thread_id = ?"
		args = append(args, thread)
	}
	return cronJobViews(r.g, query+" ORDER BY j.created_at, j.id", args...)
}

func (r reader) CronJob(id string) (account.CronJob, error) {
	var row cronRow
	if err := notFound(r.g.Raw("SELECT * FROM cron_jobs WHERE id = ?", id).Take(&row).Error); err != nil {
		return account.CronJob{}, fmt.Errorf("store: scheduled job %s: %w", id, err)
	}
	return row.job()
}

func (r reader) Due(now time.Time) ([]account.CronJob, error) {
	return each(r.g.Raw(`SELECT j.* FROM cron_jobs j JOIN threads t ON t.id = j.thread_id
		WHERE j.paused = 0 AND j.next_run_at <= ? AND t.archived_at IS NULL ORDER BY j.next_run_at, j.id`, now.UTC()), cronRow.job)
}

func (r reader) NextRun() (time.Time, bool, error) {
	var unpaused int64
	if err := r.g.Raw("SELECT count(*) FROM cron_jobs WHERE paused = 0").Scan(&unpaused).Error; err != nil || unpaused == 0 {
		return time.Time{}, false, err
	}
	var rows []cronRow
	err := r.g.Raw(`SELECT j.* FROM cron_jobs j JOIN threads t ON t.id = j.thread_id
		WHERE j.paused = 0 AND t.archived_at IS NULL ORDER BY j.next_run_at LIMIT 1`).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return time.Time{}, true, err
	}
	return rows[0].NextRunAt, true, nil
}

func (r reader) Threads(owner string, f store.ThreadFilter) ([]store.Thread, bool, error) {
	query := "SELECT * FROM threads WHERE user_id = ? AND " + listed
	args := []any{owner}
	if c := f.After; c != nil {
		query += " AND (updated_at < ? OR (updated_at = ? AND (created_at < ? OR (created_at = ? AND id < ?))))"
		args = append(args, c.Updated.UTC(), c.Updated.UTC(), c.Created.UTC(), c.Created.UTC(), c.ID)
	}
	if f.Workspace != nil {
		query += " AND workspace_id = ?"
		args = append(args, *f.Workspace)
	}
	if f.Archived {
		query += " AND archived_at IS NOT NULL"
	} else {
		query += " AND archived_at IS NULL"
	}
	out, err := threads(r.g, query+" ORDER BY updated_at DESC, created_at DESC, id DESC LIMIT ?", append(args, f.Limit+1)...)
	if err != nil || len(out) <= f.Limit {
		return out, false, err
	}
	return out[:f.Limit], true, nil
}

func (r reader) Waiting(owner string) ([]string, error) {
	var ids []string
	err := r.g.Raw("SELECT id FROM threads WHERE user_id = ? AND own_status IN ?", owner, waitsOnUser).Scan(&ids).Error
	return ids, err
}

const (
	listed  = "(parent_id IS NULL)"
	listedT = "(t.parent_id IS NULL)"
)

type todoRow struct {
	ID          int64
	Parent      int64
	Title       string
	WorkspaceID string
	ThreadID    string
	Status      string
	Notes       string
	Result      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	EndedAt     *time.Time
}

func (row todoRow) todo() (account.Todo, error) {
	return account.Todo{ID: row.ID, Parent: row.Parent, Title: row.Title, Workspace: row.WorkspaceID, Thread: row.ThreadID, Status: account.TodoStatus(row.Status),
		Notes: row.Notes, Result: row.Result, Created: row.CreatedAt, Updated: row.UpdatedAt, Ended: row.EndedAt}, nil
}

func (r reader) Todos(owner string, f store.TodoFilter) ([]account.Todo, bool, error) {
	query := "SELECT * FROM todos WHERE user_id = ?"
	args := []any{owner}
	if len(f.Statuses) > 0 {
		query += " AND status IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(f.Statuses)), ", ") + ")"
		for _, st := range f.Statuses {
			args = append(args, string(st))
		}
	}
	query += " ORDER BY ended_at IS NULL DESC, ended_at DESC, id"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit+1)
	}
	out, err := each(r.g.Raw(query, args...), todoRow.todo)
	if err != nil || f.Limit <= 0 || len(out) <= f.Limit {
		return out, false, err
	}
	return out[:f.Limit], true, nil
}

func (r reader) Todo(owner string, id int64) (account.Todo, error) {
	var row todoRow
	if err := notFound(r.g.Raw("SELECT * FROM todos WHERE user_id = ? AND id = ?", owner, id).Take(&row).Error); err != nil {
		return account.Todo{}, fmt.Errorf("store: todo %d: %w", id, err)
	}
	return row.todo()
}

func (t *tx) AddTodo(owner string, todo account.Todo) (account.Todo, error) {
	if err := t.g.Raw("SELECT coalesce(max(id), 0) + 1 FROM todos WHERE user_id = ?", owner).Scan(&todo.ID).Error; err != nil {
		return account.Todo{}, err
	}
	err := t.g.Exec(`INSERT INTO todos(user_id, id, parent, title, workspace_id, thread_id, status, notes, result, created_at, updated_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner, todo.ID, todo.Parent, todo.Title, todo.Workspace, todo.Thread, string(todo.Status), todo.Notes, todo.Result,
		todo.Created, todo.Updated, todo.Ended).Error
	if err != nil {
		return account.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Todo: &todo}})
	return todo, nil
}

func (t *tx) PutTodo(owner string, todo account.Todo) error {
	res := t.g.Exec(`UPDATE todos SET parent = ?, title = ?, workspace_id = ?, thread_id = ?, status = ?, notes = ?, result = ?, updated_at = ?, ended_at = ?
		WHERE user_id = ? AND id = ?`, todo.Parent, todo.Title, todo.Workspace, todo.Thread, string(todo.Status), todo.Notes, todo.Result, todo.Updated, todo.Ended,
		owner, todo.ID)
	if err := one(res, fmt.Sprintf("todo %d", todo.ID)); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Todo: &todo}})
	return nil
}

func (r reader) Calls(owner, workspace, tool string) ([]conversation.Item, error) {
	return decodeRows[conversation.Item](r.g, `SELECT i.body FROM items i JOIN threads t ON t.id = i.thread_id
		WHERE t.user_id = ? AND t.workspace_id = ? AND i.kind = 'toolCall' AND json_extract(i.body, '$.Call.Tool') = ?
		ORDER BY i.at, i.thread_id, i.seq`, owner, workspace, tool)
}

func (r reader) Children(thread string) ([]store.Thread, error) {
	return threads(r.g, "SELECT * FROM threads WHERE parent_id = ? ORDER BY created_at, id", thread)
}

func threads(g *gorm.DB, query string, args ...any) ([]store.Thread, error) {
	return each(g.Raw(query, args...), threadRow.thread)
}

func each[R, T any](q *gorm.DB, convert func(R) (T, error)) ([]T, error) {
	var rows []R
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		v, err := convert(row)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

const snippetRunes = 40

func (r reader) Search(owner string, terms []string, workspace string, limit int) (store.Found, error) {
	found := store.Found{Messages: []store.Hit{}}
	inWorkspace := func(query string, args []any, column string) (string, []any) {
		if workspace == "" {
			return query, args
		}
		return query + " AND " + column + " = ?", append(args, workspace)
	}
	like, likeArgs := likeAll("title", terms)
	query, args := inWorkspace("SELECT * FROM threads WHERE user_id = ? AND "+listed+like, append([]any{owner}, likeArgs...), "workspace_id")
	var err error
	if found.Threads, err = threads(r.g, query+" ORDER BY updated_at DESC, id LIMIT ?", append(args, limit)...); err != nil {
		return store.Found{}, err
	}

	query = `SELECT i.thread_id, t.title, i.id, i.turn_id, i.seq, i.kind, i.text, i.at FROM items i JOIN threads t ON t.id = i.thread_id
		WHERE t.user_id = ? AND ` + listedT + ` AND i.text <> ''`
	args = []any{owner}
	for _, t := range terms {
		query += ` AND i.text LIKE ? ESCAPE '\'`
		args = append(args, likePattern(t))
		if utf8.RuneCountInString(t) >= 3 {
			query += " AND i.rowid IN (SELECT rowid FROM items_search WHERE text LIKE ?)"
			args = append(args, "%"+t+"%")
		}
	}
	query, args = inWorkspace(query, args, "t.workspace_id")
	var hits []struct {
		ThreadID string
		Title    string
		ID       string
		TurnID   string
		Seq      int64
		Kind     string
		Text     string
		At       time.Time
	}
	if err := r.g.Raw(query+" ORDER BY i.at DESC, i.id LIMIT ?", append(args, limit)...).Scan(&hits).Error; err != nil {
		return store.Found{}, err
	}
	for _, h := range hits {
		found.Messages = append(found.Messages, store.Hit{Thread: h.ThreadID, Title: h.Title, Item: h.ID, Turn: h.TurnID, Seq: h.Seq,
			Kind: conversation.ItemKind(h.Kind), Snippet: snippet(h.Text, terms[0]), At: h.At})
	}

	like, likeArgs = likeAll("(j.name || ' ' || j.prompt)", terms)
	query, args = inWorkspace("WHERE t.user_id = ?"+like, append([]any{owner}, likeArgs...), "t.workspace_id")
	if found.CronJobs, err = cronJobViews(r.g, query+" ORDER BY j.created_at DESC, j.id LIMIT ?", append(args, limit)...); err != nil {
		return store.Found{}, err
	}
	return found, nil
}

func likeAll(column string, terms []string) (string, []any) {
	var query string
	var args []any
	for _, t := range terms {
		query += " AND " + column + ` LIKE ? ESCAPE '\'`
		args = append(args, likePattern(t))
	}
	return query, args
}

func likePattern(term string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
	return "%" + escaped + "%"
}

func snippet(text, term string) string {
	runes := []rune(text)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}
	want := []rune(strings.ToLower(term))
	at := 0
	for i := 0; i+len(want) <= len(lower); i++ {
		if string(lower[i:i+len(want)]) == string(want) {
			at = i
			break
		}
	}
	from, to := max(0, at-snippetRunes), min(len(runes), at+len(want)+snippetRunes)
	out := strings.Join(strings.Fields(string(runes[from:to])), " ")
	if from > 0 {
		out = "…" + out
	}
	if to < len(runes) {
		out += "…"
	}
	return out
}

func (r reader) Busy() (int64, error) {
	var n int64
	err := r.g.Raw("SELECT count(*) FROM threads WHERE status <> ?", string(conversation.ThreadIdle)).Scan(&n).Error
	return n, err
}

func (r reader) Active(since time.Time) (int64, error) {
	var n int64
	err := r.g.Raw("SELECT count(DISTINCT user_id) FROM threads WHERE updated_at >= ?", since.UTC()).Scan(&n).Error
	return n, err
}

func (t *tx) EnsureUser(id string, first account.Environment) error {
	var known int64
	if err := t.g.Raw("SELECT count(*) FROM users WHERE id = ?", id).Scan(&known).Error; err != nil || known > 0 {
		return err
	}
	return t.makeUser(account.User{ID: id}, first)
}

func (t *tx) CreateUser(u account.User, first account.Environment) error {
	var known int64
	if err := t.g.Raw("SELECT count(*) FROM users WHERE id = ?", u.ID).Scan(&known).Error; err != nil {
		return err
	}
	if known > 0 {
		return fmt.Errorf("store: user %s already exists", u.ID)
	}
	if err := t.makeUser(u, first); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{Owned: &store.Owned{User: &u}})
	return nil
}

func (t *tx) SetUserName(id, name string) error {
	if err := one(t.g.Exec("UPDATE users SET name = ? WHERE id = ?", name, id), "user "+id); err != nil {
		return err
	}
	u := account.User{ID: id, Name: name}
	t.owned = append(t.owned, store.Event{Owned: &store.Owned{User: &u}})
	return nil
}

// makeUser makes a user with the settings every user starts with and their
// first environment.
func (t *tx) makeUser(u account.User, first account.Environment) error {
	id := u.ID
	now := time.Now().UTC()
	if err := t.g.Exec("INSERT INTO users(id, name, created_at) VALUES (?, ?, ?)", id, u.Name, now).Error; err != nil {
		return fmt.Errorf("store: user %s: %w", id, err)
	}
	if err := t.g.Exec("INSERT INTO settings(user_id, redaction, redaction_key, memory) VALUES (?, 1, ?, 1)", id, rand.Text()).Error; err != nil {
		return fmt.Errorf("store: the settings of user %s: %w", id, err)
	}
	first.Created, first.Updated = now, now
	return t.PutEnvironment(id, first)
}

func (t *tx) SetSettings(owner string, s account.Settings) error {
	permissions, _ := json.Marshal(s.Permissions)
	res := t.g.Exec("UPDATE settings SET redaction = ?, memory = ?, learn = ?, permissions = ?, model = ?, learn_model = ? WHERE user_id = ?",
		s.Redaction, s.Memory, s.Learn, string(permissions), s.Model, s.LearnModel, owner)
	if err := one(res, "the settings of user "+owner); err != nil {
		return err
	}
	stored, err := t.Settings(owner)
	if err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Settings: &stored}})
	return nil
}

func (t *tx) PutWorkspace(owner string, w account.Workspace) error {
	settings, _ := json.Marshal(w.Settings)
	providers, _ := json.Marshal(w.Providers)
	err := t.g.Exec(`INSERT INTO workspaces(id, user_id, environment_id, name, root, moving_to, kind, instructions, providers, settings, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, root = excluded.root, moving_to = excluded.moving_to, kind = excluded.kind,
			instructions = excluded.instructions, providers = excluded.providers, settings = excluded.settings, updated_at = excluded.updated_at,
			archived_at = excluded.archived_at`,
		w.ID, owner, w.Environment, w.Name, storeRoot(w.Root, t.base), storeRoot(w.MovingTo, t.base), string(w.Kind), w.Instructions, string(providers), string(settings),
		w.Created, w.Updated, w.Archived).Error
	if err != nil {
		return fmt.Errorf("store: workspace %s: %w", w.Name, err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Workspace: &w}})
	return nil
}

// PutEnvironment makes an environment, with its scratch workspace, or
// renames it: its provider and config are what it was made with, and its
// state is the provider's (KeepEnvironmentState).
func (t *tx) PutEnvironment(owner string, e account.Environment) error {
	var known int64
	if err := t.g.Raw("SELECT count(*) FROM environments WHERE id = ? AND user_id = ?", e.ID, owner).Scan(&known).Error; err != nil {
		return err
	}
	err := t.g.Exec(`INSERT INTO environments(id, user_id, name, provider, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		e.ID, owner, e.Name, e.Provider, jsonOr(e.Config), e.Created, e.Updated).Error
	if err != nil {
		return fmt.Errorf("store: environment %s: %w", e.Name, err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Environment: &e}})
	if known > 0 {
		return nil
	}
	return t.PutWorkspace(owner, account.NewScratch(e))
}

// KeepEnvironmentState keeps what an environment's provider keeps of its
// sandbox. No one is told: the state is the provider's alone.
func (t *tx) KeepEnvironmentState(owner, id string, state json.RawMessage) error {
	return one(t.g.Exec("UPDATE environments SET state = ? WHERE id = ? AND user_id = ?", jsonOr(state), id, owner), "environment "+id)
}

// jsonOr is a JSON value to store, {} for none.
func jsonOr(v json.RawMessage) string {
	if len(bytes.TrimSpace(v)) == 0 {
		return "{}"
	}
	return string(v)
}

// DeleteEnvironment deletes an environment and its scratch workspace; no
// conversation may be in it (EnvironmentInUse).
func (t *tx) DeleteEnvironment(owner, id string) error {
	if err := t.g.Exec("DELETE FROM workspaces WHERE user_id = ? AND environment_id = ? AND kind = ?", owner, id, string(account.ScratchWorkspace)).Error; err != nil {
		return fmt.Errorf("store: the scratch workspace of environment %s: %w", id, err)
	}
	if err := one(t.g.Exec("DELETE FROM environments WHERE id = ? AND user_id = ?", id, owner), "environment "+id); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{EnvironmentDeleted: id}})
	return nil
}

func (t *tx) PutMCPServer(owner string, s account.MCPServer) error {
	args, _ := json.Marshal(nonNil(s.Args))
	env, _ := json.Marshal(nonNilMap(s.Env))
	headers, _ := json.Marshal(nonNilMap(s.Headers))
	err := t.g.Exec(`INSERT INTO mcp_servers(user_id, name, command, args, env, url, headers) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, name) DO UPDATE SET command = excluded.command, args = excluded.args, env = excluded.env,
			url = excluded.url, headers = excluded.headers`,
		owner, s.Name, s.Command, string(args), string(env), s.URL, string(headers)).Error
	if err != nil {
		return fmt.Errorf("store: MCP server %s: %w", s.Name, err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{MCPServer: &s}})
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (t *tx) DeleteMCPServer(owner, name string) error {
	if err := one(t.g.Exec("DELETE FROM mcp_servers WHERE user_id = ? AND name = ?", owner, name), "MCP server "+name); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{MCPServerDeleted: name}})
	return nil
}

func (t *tx) PutCronJob(j account.CronJob) error {
	th, err := t.Thread(j.Thread)
	if err != nil {
		return err
	}
	err = t.g.Exec(`INSERT INTO cron_jobs(id, thread_id, name, spec, prompt, times, runs, paused, next_run_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET spec = excluded.spec, prompt = excluded.prompt, times = excluded.times, runs = excluded.runs,
			paused = excluded.paused, next_run_at = excluded.next_run_at`,
		j.ID, j.Thread, j.Name, j.Spec, j.Prompt, j.Times, j.Runs, j.Paused, j.NextRun.UTC(), j.Created).Error
	if err != nil {
		return fmt.Errorf("store: scheduled job %s: %w", j.Name, err)
	}
	put, err := cronJobViews(t.g, "WHERE j.id = ?", j.ID)
	if err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: th.User, Thread: j.Thread, Owned: &store.Owned{CronJob: &put[0]}})
	return nil
}

func (t *tx) DeleteCronJob(id string) error {
	j, err := t.CronJob(id)
	if err != nil {
		return err
	}
	th, err := t.Thread(j.Thread)
	if err != nil {
		return err
	}
	if err := t.g.Exec("DELETE FROM cron_jobs WHERE id = ?", id).Error; err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: th.User, Thread: j.Thread, Owned: &store.Owned{CronJobDeleted: id}})
	return nil
}

func (t *tx) DeleteThread(id string) error {
	th, err := t.Thread(id)
	if err != nil {
		return err
	}
	var jobs []struct{ ID, ThreadID string }
	err = t.g.Raw("SELECT id, thread_id FROM cron_jobs WHERE thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_id = ?)", id, id).
		Scan(&jobs).Error
	if err != nil {
		return err
	}
	var linked []todoRow
	err = t.g.Raw(`SELECT * FROM todos WHERE user_id = ? AND (thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_id = ?))`,
		th.User, id, id).Scan(&linked).Error
	if err != nil {
		return err
	}
	if len(linked) > 0 {
		now := time.Now().UTC()
		res := t.g.Exec(`UPDATE todos SET thread_id = '', workspace_id = '',
			status = CASE WHEN status = 'handed' THEN 'open' ELSE status END, updated_at = ?
			WHERE user_id = ? AND (thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_id = ?))`, now, th.User, id, id)
		if res.Error != nil {
			return fmt.Errorf("store: clear the todos of thread %s: %w", id, res.Error)
		}
		for _, row := range linked {
			todo, err := row.todo()
			if err != nil {
				return err
			}
			todo.Thread, todo.Workspace = "", ""
			if todo.Status == account.TodoHanded {
				todo.Status = account.TodoOpen
			}
			todo.Updated = now
			t.owned = append(t.owned, store.Event{User: th.User, Owned: &store.Owned{Todo: &todo}})
		}
	}
	if err := t.g.Exec("DELETE FROM threads WHERE id = ?", id).Error; err != nil {
		return fmt.Errorf("store: delete thread %s: %w", id, err)
	}
	t.pendingFor(id, th.User).change.Deleted = true
	for _, j := range jobs {
		t.owned = append(t.owned, store.Event{User: th.User, Thread: j.ThreadID, Owned: &store.Owned{CronJobDeleted: j.ID}})
	}
	return nil
}

func (t *tx) DeleteUser(id string) ([]string, error) {
	var busy int64
	if err := t.g.Raw("SELECT count(*) FROM threads WHERE user_id = ? AND status <> ?", id, string(conversation.ThreadIdle)).Scan(&busy).Error; err != nil {
		return nil, err
	}
	if busy > 0 {
		return nil, fmt.Errorf("store: user %s has a turn in progress or queued", id)
	}
	var threads []string
	if err := t.g.Raw("SELECT id FROM threads WHERE user_id = ?", id).Scan(&threads).Error; err != nil {
		return nil, err
	}
	var roots []string
	if err := t.g.Raw("SELECT id FROM threads WHERE user_id = ? AND parent_id IS NULL", id).Scan(&roots).Error; err != nil {
		return nil, err
	}
	for _, thread := range roots {
		if err := t.DeleteThread(thread); err != nil {
			return nil, err
		}
	}
	if err := t.g.Exec("DELETE FROM usage WHERE user_id = ?", id).Error; err != nil {
		return nil, err
	}
	res := t.g.Exec("DELETE FROM users WHERE id = ?", id)
	if err := one(res, "user "+id); err != nil {
		return nil, err
	}
	return threads, nil
}

func one(res *gorm.DB, what string) error {
	if res.Error != nil {
		return fmt.Errorf("store: %s: %w", what, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("store: %s: %w", what, store.ErrNotFound)
	}
	return nil
}

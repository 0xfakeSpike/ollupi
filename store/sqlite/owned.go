package sqlite

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r reader) Users() ([]string, error) {
	var out []string
	err := r.g.Raw("SELECT id FROM users ORDER BY id").Scan(&out).Error
	return out, err
}

type settingsRow struct {
	Redaction    bool
	RedactionKey string
	Memory       bool
	Permissions  string
}

func (r reader) Settings(owner string) (user.Settings, error) {
	var row settingsRow
	if err := notFound(r.g.Raw("SELECT * FROM settings WHERE user_id = ?", owner).Take(&row).Error); err != nil {
		return user.Settings{}, fmt.Errorf("store: the settings of user %s: %w", owner, err)
	}
	s := user.Settings{Redaction: row.Redaction, RedactionKey: row.RedactionKey, Memory: row.Memory}
	if err := json.Unmarshal([]byte(row.Permissions), &s.Permissions); err != nil {
		return user.Settings{}, fmt.Errorf("store: the permissions of user %s: %w", owner, err)
	}
	return s, nil
}

type workspaceRow struct {
	ID         string
	Name       string
	Root       string
	Settings   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

func (row workspaceRow) workspace() (user.Workspace, error) {
	w := user.Workspace{ID: row.ID, Name: row.Name, Root: row.Root, Created: row.CreatedAt, Updated: row.UpdatedAt, Archived: row.ArchivedAt}
	if err := json.Unmarshal([]byte(row.Settings), &w.Settings); err != nil {
		return user.Workspace{}, fmt.Errorf("store: the settings of workspace %s: %w", row.ID, err)
	}
	return w, nil
}

func (r reader) Workspaces(owner string) ([]user.Workspace, error) {
	return each(r.g.Raw("SELECT * FROM workspaces WHERE user_id = ? ORDER BY name, id", owner), workspaceRow.workspace)
}

func (r reader) Workspace(owner, id string) (user.Workspace, error) {
	var row workspaceRow
	if err := notFound(r.g.Raw("SELECT * FROM workspaces WHERE id = ? AND user_id = ?", id, owner).Take(&row).Error); err != nil {
		return user.Workspace{}, fmt.Errorf("store: workspace %s: %w", id, err)
	}
	return row.workspace()
}

type environmentRow struct {
	ID        string
	Name      string
	Kind      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (row environmentRow) environment() (user.Environment, error) {
	return user.Environment{ID: row.ID, Name: row.Name, Kind: user.EnvironmentKind(row.Kind), Created: row.CreatedAt, Updated: row.UpdatedAt}, nil
}

func (r reader) Environments(owner string) ([]user.Environment, error) {
	return each(r.g.Raw("SELECT * FROM environments WHERE user_id = ? ORDER BY kind <> ?, created_at, id", owner, string(user.Local)), environmentRow.environment)
}

func (r reader) Environment(owner, id string) (user.Environment, error) {
	var row environmentRow
	if err := notFound(r.g.Raw("SELECT * FROM environments WHERE id = ? AND user_id = ?", id, owner).Take(&row).Error); err != nil {
		return user.Environment{}, fmt.Errorf("store: environment %s: %w", id, err)
	}
	return row.environment()
}

func (r reader) EnvironmentInUse(owner, id string) (bool, error) {
	var n int64
	err := r.g.Raw("SELECT count(*) FROM threads WHERE user_id = ? AND environment_id = ?", owner, id).Scan(&n).Error
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

func (r reader) MCPServers(owner string) ([]user.MCPServer, error) {
	var rows []mcpRow
	if err := r.g.Raw("SELECT * FROM mcp_servers WHERE user_id = ? ORDER BY name", owner).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]user.MCPServer, len(rows))
	for i, row := range rows {
		s := user.MCPServer{Name: row.Name, Command: row.Command, URL: row.URL}
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

func (row cronRow) job() (user.CronJob, error) {
	return user.CronJob{ID: row.ID, Thread: row.ThreadID, Name: row.Name, Spec: row.Spec, Prompt: row.Prompt, Times: row.Times,
		Runs: row.Runs, Paused: row.Paused, NextRun: row.NextRunAt, Created: row.CreatedAt}, nil
}

type taskRow struct {
	Job         cronRow `gorm:"embedded"`
	Title       string
	WorkspaceID *string
	ArchivedAt  *time.Time
	LastTurn    *string
}

const tasksFrom = `SELECT j.*, t.title, t.workspace_id, t.archived_at,
		(SELECT u.body FROM turns u WHERE u.cron_job = j.id ORDER BY u.ord DESC LIMIT 1) AS last_turn
	FROM cron_jobs j JOIN threads t ON t.id = j.thread_id`

func (row taskRow) task() (store.Task, error) {
	job, _ := row.Job.job()
	t := store.Task{CronJob: job, Title: row.Title, Workspace: deref(row.WorkspaceID), Archived: row.ArchivedAt != nil}
	if row.LastTurn == nil {
		return t, nil
	}
	var last conversation.Turn
	if err := json.Unmarshal([]byte(*row.LastTurn), &last); err != nil {
		return store.Task{}, fmt.Errorf("store: the last run of task %s: %w", job.ID, err)
	}
	t.LastTurn, t.LastRun = last.ID, &last.QueuedAt
	return t, nil
}

func tasks(g *gorm.DB, query string, args ...any) ([]store.Task, error) {
	return each(g.Raw(tasksFrom+" "+query, args...), taskRow.task)
}

func (r reader) CronJobs(owner, thread string) ([]store.Task, error) {
	query := "WHERE t.user_id = ?"
	args := []any{owner}
	if thread != "" {
		query += " AND j.thread_id = ?"
		args = append(args, thread)
	}
	return tasks(r.g, query+" ORDER BY j.created_at, j.id", args...)
}

func (r reader) CronJob(id string) (user.CronJob, error) {
	var row cronRow
	if err := notFound(r.g.Raw("SELECT * FROM cron_jobs WHERE id = ?", id).Take(&row).Error); err != nil {
		return user.CronJob{}, fmt.Errorf("store: scheduled task %s: %w", id, err)
	}
	return row.job()
}

func (r reader) Due(now time.Time) ([]user.CronJob, error) {
	return each(r.g.Raw(`SELECT j.* FROM cron_jobs j JOIN threads t ON t.id = j.thread_id
		WHERE j.paused = 0 AND j.next_run_at <= ? AND t.archived_at IS NULL ORDER BY j.next_run_at, j.id`, now.UTC()), cronRow.job)
}

func (r reader) Threads(owner string, f store.ThreadFilter) ([]store.Thread, bool, error) {
	query := "SELECT * FROM threads WHERE user_id = ? AND parent_id IS NULL"
	args := []any{owner}
	if c := f.After; c != nil {
		query += " AND (updated_at < ? OR (updated_at = ? AND (created_at < ? OR (created_at = ? AND id < ?))))"
		args = append(args, c.Updated.UTC(), c.Updated.UTC(), c.Created.UTC(), c.Created.UTC(), c.ID)
	}
	if f.Workspace != nil {
		if *f.Workspace == "" {
			query += " AND workspace_id IS NULL"
		} else {
			query += " AND workspace_id = ?"
			args = append(args, *f.Workspace)
		}
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
	query, args := inWorkspace("SELECT * FROM threads WHERE user_id = ? AND parent_id IS NULL"+like, append([]any{owner}, likeArgs...), "workspace_id")
	var err error
	if found.Threads, err = threads(r.g, query+" ORDER BY updated_at DESC, id LIMIT ?", append(args, limit)...); err != nil {
		return store.Found{}, err
	}

	query = `SELECT i.thread_id, t.title, i.id, i.turn_id, i.seq, i.kind, i.text, i.at FROM items i JOIN threads t ON t.id = i.thread_id
		WHERE t.user_id = ? AND t.parent_id IS NULL AND i.text <> ''`
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
	if found.Tasks, err = tasks(r.g, query+" ORDER BY j.created_at DESC, j.id LIMIT ?", append(args, limit)...); err != nil {
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

func (t *tx) EnsureUser(id string) error {
	var known int64
	if err := t.g.Raw("SELECT count(*) FROM users WHERE id = ?", id).Scan(&known).Error; err != nil || known > 0 {
		return err
	}
	now := time.Now().UTC()
	if err := t.g.Exec("INSERT INTO users(id, created_at) VALUES (?, ?)", id, now).Error; err != nil {
		return fmt.Errorf("store: user %s: %w", id, err)
	}
	if err := t.g.Exec("INSERT INTO settings(user_id, redaction, redaction_key, memory) VALUES (?, 1, ?, 1)", id, rand.Text()).Error; err != nil {
		return fmt.Errorf("store: the settings of user %s: %w", id, err)
	}
	return t.PutEnvironment(id, user.Environment{ID: uuid.NewString(), Name: user.LocalName, Kind: user.Local, Created: now, Updated: now})
}

func (t *tx) SetSettings(owner string, s user.Settings) error {
	permissions, _ := json.Marshal(s.Permissions)
	res := t.g.Exec("UPDATE settings SET redaction = ?, memory = ?, permissions = ? WHERE user_id = ?", s.Redaction, s.Memory, string(permissions), owner)
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

func (t *tx) PutWorkspace(owner string, w user.Workspace) error {
	settings, _ := json.Marshal(w.Settings)
	err := t.g.Exec(`INSERT INTO workspaces(id, user_id, name, root, settings, created_at, updated_at, archived_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, root = excluded.root, settings = excluded.settings,
			updated_at = excluded.updated_at, archived_at = excluded.archived_at`,
		w.ID, owner, w.Name, w.Root, string(settings), w.Created, w.Updated, w.Archived).Error
	if err != nil {
		return fmt.Errorf("store: workspace %s: %w", w.Name, err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Workspace: &w}})
	return nil
}

func (t *tx) PutEnvironment(owner string, e user.Environment) error {
	err := t.g.Exec(`INSERT INTO environments(id, user_id, name, kind, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		e.ID, owner, e.Name, string(e.Kind), e.Created, e.Updated).Error
	if err != nil {
		return fmt.Errorf("store: environment %s: %w", e.Name, err)
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Environment: &e}})
	return nil
}

func (t *tx) DeleteEnvironment(owner, id string) error {
	if err := one(t.g.Exec("DELETE FROM environments WHERE id = ? AND user_id = ?", id, owner), "environment "+id); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{EnvironmentDeleted: id}})
	return nil
}

func (t *tx) PutMCPServer(owner string, s user.MCPServer) error {
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

func (t *tx) PutCronJob(j user.CronJob) error {
	th, err := t.Thread(j.Thread)
	if err != nil {
		return err
	}
	err = t.g.Exec(`INSERT INTO cron_jobs(id, thread_id, name, spec, prompt, times, runs, paused, next_run_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET spec = excluded.spec, prompt = excluded.prompt, times = excluded.times, runs = excluded.runs,
			paused = excluded.paused, next_run_at = excluded.next_run_at`,
		j.ID, j.Thread, j.Name, j.Spec, j.Prompt, j.Times, j.Runs, j.Paused, j.NextRun.UTC(), j.Created).Error
	if err != nil {
		return fmt.Errorf("store: scheduled task %s: %w", j.Name, err)
	}
	put, err := tasks(t.g, "WHERE j.id = ?", j.ID)
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
	var jobs []string
	if err := t.g.Raw("SELECT id FROM cron_jobs WHERE thread_id = ?", id).Scan(&jobs).Error; err != nil {
		return err
	}
	if err := t.g.Exec("DELETE FROM threads WHERE id = ?", id).Error; err != nil {
		return fmt.Errorf("store: delete thread %s: %w", id, err)
	}
	t.note(id, th.User).change.Deleted = true
	for _, j := range jobs {
		t.owned = append(t.owned, store.Event{User: th.User, Thread: id, Owned: &store.Owned{CronJobDeleted: j}})
	}
	return nil
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

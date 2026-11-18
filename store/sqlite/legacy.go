package sqlite

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed legacy/*.sql
var legacyMigrations embed.FS

const legacyFile = "ollupi.sqlite"

const upgraded = "ended by the upgrade to ollupi v0.16.0"

func (s *Store) importLegacy(home string) error {
	old := filepath.Join(home, "data", legacyFile)
	if _, err := os.Stat(old); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var users int64
	if err := s.g.Raw("SELECT count(*) FROM users").Scan(&users).Error; err != nil || users > 0 {
		return err
	}
	copied := filepath.Join(home, "data", "ollupi-import.sqlite")
	defer removeDB(copied)
	removeDB(copied)
	if err := snapshot(old, copied); err != nil {
		return fmt.Errorf("store: copy %s to import it: %w", old, err)
	}
	g, err := sqlitedb.Open(copied)
	if err != nil {
		return err
	}
	defer sqlitedb.Close(g)
	if err := migrateLegacy(g); err != nil {
		return fmt.Errorf("store: bring %s to its last schema: %w", old, err)
	}
	return s.Update(context.Background(), func(t store.Tx) error { return legacy{g: g, t: t.(*tx)}.bring() })
}

func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}
}

func snapshot(from, to string) error {
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(from), RawQuery: "mode=ro"}).String()
	g, err := gorm.Open(gormsqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	defer sqlitedb.Close(g)
	return g.Exec("VACUUM INTO ?", to).Error
}

func migrateLegacy(g *gorm.DB) error {
	version, err := sqlitedb.Version(g)
	if err != nil {
		return err
	}
	if version == 0 {
		return errors.New("the database is from an ollupi before v0.8.0; open it once with ollupi v0.15.1, then again with this one")
	}
	return sqlitedb.Migrate(g, legacyMigrations, "legacy")
}

type legacy struct {
	g *gorm.DB
	t *tx
}

func (l legacy) bring() error {
	for _, step := range []func() error{l.users, l.environments, l.workspaces, l.mcpServers, l.threads, l.cronJobs, l.memories} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (l legacy) users() error {
	var rows []struct {
		ID           string
		CreatedAt    time.Time
		Redaction    bool
		RedactionKey string
		Memory       bool
		Permissions  string
	}
	err := l.g.Raw(`SELECT u.id, u.created_at, s.redaction, s.redaction_key, s.memory, s.permissions
		FROM users u JOIN settings s ON s.user_id = u.id`).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("store: the old users: %w", err)
	}
	for _, r := range rows {
		if err := l.t.g.Exec("INSERT INTO users(id, created_at) VALUES (?, ?)", r.ID, r.CreatedAt.UTC()).Error; err != nil {
			return err
		}
		err := l.t.g.Exec("INSERT INTO settings(user_id, redaction, redaction_key, memory, permissions) VALUES (?, ?, ?, ?, ?)",
			r.ID, r.Redaction, r.RedactionKey, r.Memory, cmp.Or(r.Permissions, "{}")).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (l legacy) environments() error {
	return l.copy("environments", "SELECT id, user_id, name, kind, created_at, updated_at FROM environments",
		"INSERT INTO environments(id, user_id, name, kind, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)")
}

func (l legacy) workspaces() error {
	return l.copy("workspaces", "SELECT id, user_id, name, root_path, settings, created_at, updated_at, archived_at FROM workspaces",
		"INSERT INTO workspaces(id, user_id, name, root, settings, created_at, updated_at, archived_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
}

func (l legacy) mcpServers() error {
	return l.copy("MCP servers", "SELECT user_id, name, command, args, env, url, headers FROM mcp_servers",
		"INSERT INTO mcp_servers(user_id, name, command, args, env, url, headers) VALUES (?, ?, ?, ?, ?, ?, ?)")
}

func (l legacy) cronJobs() error {
	return l.copy("tasks", "SELECT id, thread_id, name, spec, prompt, times, runs, paused, next_run_at, created_at FROM cron_jobs",
		"INSERT INTO cron_jobs(id, thread_id, name, spec, prompt, times, runs, paused, next_run_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
}

func (l legacy) copy(what, read, write string) error {
	rows, err := l.g.Raw(read).Rows()
	if err != nil {
		return fmt.Errorf("store: the old %s: %w", what, err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("store: the old %s: %w", what, err)
		}
		if err := l.t.g.Exec(write, values...).Error; err != nil {
			return fmt.Errorf("store: an old row of %s: %w", what, err)
		}
	}
	return rows.Err()
}

type legacyThread struct {
	ID             string
	UserID         string
	WorkspaceID    *string
	EnvironmentID  *string
	Title          string
	Settings       string
	ParentThreadID *string
	ParentItemID   *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
	RepliedAt      *time.Time
	ReadAt         *time.Time
}

func (l legacy) threads() error {
	var threads []legacyThread
	err := l.g.Raw(`SELECT id, user_id, workspace_id, environment_id, title, settings, parent_thread_id, parent_item_id,
		created_at, updated_at, archived_at, replied_at, read_at FROM threads ORDER BY parent_thread_id IS NOT NULL, created_at, id`).Scan(&threads).Error
	if err != nil {
		return fmt.Errorf("store: the old conversations: %w", err)
	}
	children := map[string]string{}
	for _, th := range threads {
		if th.ParentItemID != nil {
			children[*th.ParentItemID] = th.ID
		}
	}
	for _, th := range threads {
		var settings policy.Settings
		json.Unmarshal([]byte(th.Settings), &settings)
		header := store.Thread{User: th.UserID, Thread: user.Thread{ID: th.ID, Title: th.Title, Settings: settings,
			Workspace: deref(th.WorkspaceID), Environment: deref(th.EnvironmentID), Parent: deref(th.ParentThreadID),
			ParentItem: deref(th.ParentItemID), Created: th.CreatedAt.UTC(), Updated: th.UpdatedAt.UTC()}}
		if err := l.t.CreateThread(header); err != nil {
			return err
		}
		entries, err := l.log(th.ID, children)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			if _, err := l.t.Append(th.ID, entries...); err != nil {
				return fmt.Errorf("store: the log of old conversation %s: %w", th.ID, err)
			}
		}
		err = l.t.g.Exec("UPDATE threads SET updated_at = ?, archived_at = ?, replied_at = ?, read_at = ? WHERE id = ?",
			th.UpdatedAt.UTC(), utc(th.ArchivedAt), utc(th.RepliedAt), utc(th.ReadAt), th.ID).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

type legacyTurn struct {
	ID          string
	Status      string
	Model       string
	Agent       string
	Error       string
	CronJobID   string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

type legacyItem struct {
	ID                string
	TurnID            string
	Type              string
	Status            string
	CallID            string
	LaunchItemID      string
	Tool              string
	Server            string
	ResponseID        string
	Error             string
	StartedAt         time.Time
	CompletedAt       *time.Time
	Approval          *string
	Await             *string
	Annotations       *string
	Usage             *string
	ContextStats      *string
	Memory            *string
	Attachments       *string
	Text              string
	Arguments         string
	PreparedExecution *string
	Result            *string
	Native            []byte
}

type legacyApproval struct {
	ID          string                `json:"id"`
	RequestedAt time.Time             `json:"requestedAt"`
	Reason      string                `json:"reason"`
	Rule        string                `json:"rule"`
	Content     string                `json:"content"`
	Suggested   []string              `json:"suggestedRules"`
	Decision    conversation.Decision `json:"decision"`
	DecidedBy   string                `json:"decidedBy"`
	DecidedAt   *time.Time            `json:"decidedAt"`
}

type legacyAwait struct {
	By      string          `json:"by"`
	Request json.RawMessage `json:"request"`
}

type legacyResult struct {
	Output    json.RawMessage         `json:"output"`
	Artifacts []conversation.Artifact `json:"artifacts"`
}

func (l legacy) log(thread string, children map[string]string) ([]conversation.Entry, error) {
	var turns []legacyTurn
	if err := l.g.Raw("SELECT id, status, model, agent, error, cron_job_id, created_at, completed_at FROM turns WHERE thread_id = ? ORDER BY seq", thread).Scan(&turns).Error; err != nil {
		return nil, fmt.Errorf("store: the turns of old conversation %s: %w", thread, err)
	}
	var items []legacyItem
	if err := l.g.Raw("SELECT * FROM items WHERE thread_id = ? ORDER BY turn_seq, seq", thread).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("store: the items of old conversation %s: %w", thread, err)
	}
	b := &logBuilder{children: children}
	for _, turn := range turns {
		b.turn(turn, items)
	}
	return b.entries, nil
}

type logBuilder struct {
	children map[string]string
	v        conversation.View
	entries  []conversation.Entry
}

func (b *logBuilder) add(turn, item string, at time.Time, body conversation.Body) {
	e := conversation.Entry{Seq: b.v.Head + 1, Turn: turn, Item: item, At: at.UTC(), Body: body}
	if conversation.Check(b.v, e) != nil {
		return
	}
	b.v.Apply(e)
	b.entries = append(b.entries, e)
}

func decode[T any](raw *string) (T, bool) {
	var v T
	if raw == nil || *raw == "" || *raw == "null" {
		return v, false
	}
	return v, json.Unmarshal([]byte(*raw), &v) == nil
}

func rawOf(raw *string) json.RawMessage {
	if raw == nil || *raw == "" || *raw == "null" {
		return nil
	}
	return json.RawMessage(*raw)
}

func (b *logBuilder) turn(t legacyTurn, all []legacyItem) {
	var model llm.Model
	json.Unmarshal([]byte(t.Model), &model)
	b.add(t.ID, "", t.CreatedAt, conversation.TurnQueued{Model: model, Role: t.Agent, CronJob: t.CronJobID})
	var items []legacyItem
	for _, it := range all {
		if it.TurnID == t.ID {
			items = append(items, it)
		}
	}
	responses := map[string]conversation.ModelResponded{}
	for _, it := range items {
		if it.ResponseID == "" {
			continue
		}
		r := responses[it.ResponseID]
		r.Response = it.ResponseID
		if u, ok := decode[conversation.Usage](it.Usage); ok {
			r.Usage = u
		}
		if s, ok := decode[conversation.ContextStats](it.ContextStats); ok {
			r.Stats = s
		}
		if len(it.Native) > 0 {
			r.Native = it.Native
		}
		r.Compaction = r.Compaction || it.Type == "contextCompaction"
		responses[it.ResponseID] = r
	}
	responded := map[string]bool{}
	respond := func(it legacyItem) {
		if it.ResponseID == "" || responded[it.ResponseID] {
			return
		}
		responded[it.ResponseID] = true
		b.add(t.ID, "", it.StartedAt, responses[it.ResponseID])
	}
	last := t.CreatedAt
	for _, it := range items {
		last = it.StartedAt
		if it.CompletedAt != nil {
			last = *it.CompletedAt
		}
		b.item(t.ID, it, respond)
	}
	b.end(t, last)
}

func (b *logBuilder) item(turn string, it legacyItem, respond func(legacyItem)) {
	at, ended := it.StartedAt, it.StartedAt
	if it.CompletedAt != nil {
		ended = *it.CompletedAt
	}
	result, _ := decode[legacyResult](it.Result)
	switch it.Type {
	case "userMessage":
		atts, _ := decode[[]conversation.Attachment](it.Attachments)
		b.add(turn, it.ID, at, conversation.UserMessage{Text: it.Text, Attachments: atts})
	case "reasoning", "agentMessage":
		if it.Text != "" {
			respond(it)
			var body conversation.Body = conversation.AgentMessage{Text: it.Text, Response: it.ResponseID}
			if it.Type == "reasoning" {
				body = conversation.Reasoning{Text: it.Text, Response: it.ResponseID, Started: at, Ended: ended}
			}
			b.add(turn, it.ID, at, body)
		}
		if it.Status == "failed" && it.Error != "" {
			b.add(turn, it.ID+":failed", ended, conversation.ModelFailed{Error: it.Error})
		}
	case "toolCall":
		respond(it)
		b.call(turn, it, result, at, ended)
	case "contextCompaction":
		if it.Status != "completed" {
			return
		}
		respond(it)
		stats, _ := decode[conversation.ContextStats](it.ContextStats)
		b.add(turn, it.ID, ended, conversation.Compacted{Summary: it.Text, Through: b.v.Head, Response: it.ResponseID, Stats: stats})
	case "memoryContext":
		refs, _ := decode[[]conversation.MemoryRef](it.Memory)
		b.add(turn, it.ID, at, conversation.Recalled{Text: it.Text, Memories: refs})
	case "error":
		if strings.HasPrefix(it.Text, "[runtime]") {
			b.add(turn, it.ID, at, conversation.Notice{Text: it.Text})
			return
		}
		b.add(turn, it.ID, at, conversation.ModelFailed{Error: cmp.Or(it.Error, it.Text)})
	case "agentReport":
		launch := it.LaunchItemID
		report := conversation.ChildReported{Launch: launch, Child: b.children[launch], Task: it.Text, Output: result.Output}
		if it.Status == "failed" {
			report.Output, report.Error = nil, cmp.Or(it.Error, "the subagent did not finish")
		}
		b.add(turn, it.ID, ended, report)
	}
}

func (b *logBuilder) call(turn string, it legacyItem, result legacyResult, at, ended time.Time) {
	b.add(turn, it.ID, at, conversation.ToolCall{Call: it.CallID, Tool: it.Tool, Server: it.Server, Arguments: it.Arguments,
		Response: it.ResponseID, Annotations: rawOf(it.Annotations), Prepared: rawOf(it.PreparedExecution)})
	if a, ok := decode[legacyApproval](it.Approval); ok {
		b.add(turn, it.ID, a.RequestedAt, conversation.ApprovalAsked{Approval: cmp.Or(a.ID, it.ID), Reason: a.Reason, Rule: a.Rule,
			Content: a.Content, Suggested: a.Suggested})
		if a.Decision != "" {
			decidedAt := ended
			if a.DecidedAt != nil {
				decidedAt = *a.DecidedAt
			}
			b.add(turn, it.ID, decidedAt, conversation.Decided{Decision: a.Decision, By: a.DecidedBy})
		}
	}
	await, awaited := decode[legacyAwait](it.Await)
	asked := awaited && await.By == "user"
	child := b.children[it.ID]
	switch {
	case it.Status == "pending" || it.Status == "inProgress":
	case child != "" && it.Status == "completed":
		b.add(turn, it.ID, at, conversation.ToolStarted{})
		b.add(turn, it.ID, at, conversation.ChildLaunched{Child: child})
		b.add(turn, it.ID, ended, conversation.ToolResult{Output: result.Output})
	case asked:
		b.add(turn, it.ID, at, conversation.ToolStarted{})
		b.add(turn, it.ID, at, conversation.InputAsked{Request: await.Request})
		answered := conversation.Answered{Output: result.Output}
		if it.Status != "completed" {
			answered = conversation.Answered{Skipped: true}
		}
		b.add(turn, it.ID, ended, answered)
	case it.Status == "completed":
		b.add(turn, it.ID, at, conversation.ToolStarted{})
		b.add(turn, it.ID, ended, conversation.ToolResult{Output: result.Output, Artifacts: result.Artifacts})
	case it.Status == "declined":
		b.add(turn, it.ID, ended, conversation.ToolResult{Declined: true, Error: cmp.Or(it.Error, "declined")})
	default:
		b.add(turn, it.ID, ended, conversation.ToolResult{Error: cmp.Or(it.Error, "the call failed")})
	}
}

func (b *logBuilder) end(t legacyTurn, last time.Time) {
	status, reason := conversation.End(t.Status), t.Error
	at := last
	if t.CompletedAt != nil {
		at = *t.CompletedAt
	}
	switch status {
	case conversation.Completed, conversation.Failed, conversation.Interrupted:
	default:
		status, reason = conversation.Interrupted, upgraded
	}
	why := cmp.Or(reason, "the turn ended before the call did")
	for _, it := range b.v.ItemsOf(t.ID) {
		switch {
		case it.WaitsForInput():
			b.add(t.ID, it.ID, at, conversation.Answered{Skipped: true})
		case it.Open():
			b.add(t.ID, it.ID, at, conversation.ToolResult{Declined: true, Error: why})
		}
	}
	b.add(t.ID, "", at, conversation.TurnEnded{Status: status, Reason: reason})
}

func (l legacy) memories() error {
	var rows []struct {
		ID, UserID, Scope, WorkspaceID, OriginWorkspaceID, Topic, Kind, Text, Status, Sources string
		UserEdited                                                                            bool
		CreatedAt, UpdatedAt                                                                  time.Time
	}
	if err := l.g.Raw("SELECT * FROM memories").Scan(&rows).Error; err != nil {
		return fmt.Errorf("store: the old memories: %w", err)
	}
	var located []struct{ ID, ThreadID string }
	if err := l.g.Raw("SELECT id, thread_id FROM items").Scan(&located).Error; err != nil {
		return err
	}
	threadOf := map[string]string{}
	for _, it := range located {
		threadOf[it.ID] = it.ThreadID
	}
	for _, r := range rows {
		var old []struct {
			ID string `json:"id"`
		}
		json.Unmarshal([]byte(r.Sources), &old)
		sources := []memory.Source{}
		for _, s := range old {
			if thread, ok := threadOf[s.ID]; ok {
				sources = append(sources, memory.Source{Thread: thread, Item: s.ID})
			}
		}
		m := memory.Memory{ID: r.ID, User: r.UserID, Place: memory.Place{Scope: memory.Scope(r.Scope), Workspace: r.WorkspaceID}, Origin: r.OriginWorkspaceID,
			Topic: r.Topic, Kind: memory.Kind(r.Kind), Text: r.Text, Status: memory.Status(r.Status), UserEdited: r.UserEdited,
			Sources: sources, Created: r.CreatedAt.UTC(), Updated: r.UpdatedAt.UTC()}
		if err := l.t.PutMemory(m); err != nil {
			return err
		}
	}
	users, err := l.t.Users()
	if err != nil {
		return err
	}
	for _, u := range users {
		ended, err := l.t.EndedTurns(u, memory.Cursor{}, math.MaxInt32)
		if err != nil {
			return err
		}
		if len(ended) == 0 {
			continue
		}
		if err := l.t.SetMemoryState(u, memory.State{Cursor: ended[len(ended)-1]}); err != nil {
			return err
		}
	}
	return nil
}

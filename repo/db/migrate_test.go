package db

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/sqlitedb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// rowTypes are the rows of the schema, as gorm would make it from them.
var rowTypes = []any{
	&UserRow{}, &EnvironmentRow{}, &WorkspaceRow{}, &ThreadRow{}, &TurnRow{}, &ItemRow{}, &CronJobRow{},
	&MemoryRow{}, &MemoryStateRow{}, &SettingsRow{}, &ShardLeaseRow{},
}

// The migrations and the row types are two accounts of one schema: a fresh
// database after every step has the tables, columns, keys, checks and
// indexes gorm would make from the rows.
func TestMigrationsMatchTheRows(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gormed := rawDB(t, filepath.Join(t.TempDir(), "gorm.sqlite"))
	if err := gormed.AutoMigrate(rowTypes...); err != nil {
		t.Fatal(err)
	}
	got, want := describe(t, db.gorm), describe(t, gormed)
	if len(want) < len(rowTypes) {
		t.Fatalf("the rows describe only %d lines", len(want))
	}
	for _, line := range diff(got, want) {
		t.Error(line)
	}
}

func TestADatabaseFromBeforeMigrationsKeepsItsRows(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// gorm made the database of the last ollupi before migrations as the first step does, with no schema version.
	old := rawDB(t, path)
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Exec(all[0]).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	done := at.Add(time.Minute)
	if err := old.Exec("INSERT INTO threads (id, title, created_at, updated_at) VALUES ('kept', 'kept', ?, ?)", at, at).Error; err != nil {
		t.Fatal(err)
	}
	if err := old.Exec(`INSERT INTO turns (id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens,
		output_tokens, started_at, completed_at) VALUES ('said', 'kept', 1, 'completed', '{"name":"m","connection":"c"}', 0, 0, 0, 0, ?, ?)`, at, done).Error; err != nil {
		t.Fatal(err)
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := userVersion(t, db.gorm); v != stepCount(t) {
		t.Fatalf("schema version %d, want %d", v, stepCount(t))
	}
	var n int64
	if err := db.gorm.Model(&ThreadRow{}).Where("id = ?", "kept").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("the thread from before: %d rows, %v", n, err)
	}
	// What it said before counts as read: the step does not wake a database up with every thread unread.
	kept, err := db.ThreadRow("kept")
	if err != nil {
		t.Fatal(err)
	}
	if kept.RepliedAt == nil || kept.Unread() {
		t.Errorf("the thread from before is unread (replied %v, read %v)", kept.RepliedAt, kept.ReadAt)
	}
}

func TestADatabaseOlderThanTheFirstStepIsRefused(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := old.Exec("CREATE TABLE workspaces (id text PRIMARY KEY, name text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	closeRaw(t, old)
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "v0.8.1") {
		t.Fatalf("opening a database older than the first step: %v", err)
	}
}

func TestADatabaseFromANewerOllupiIsRefused(t *testing.T) {
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.gorm.Exec(fmt.Sprintf("PRAGMA user_version = %d", stepCount(t)+1)).Error; err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("opening a newer database: %v", err)
	}
}

func rawDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file:"+path+"?_foreign_keys=on"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRaw(t, g) })
	return g
}

func closeRaw(t *testing.T, g *gorm.DB) {
	t.Helper()
	if conn, err := g.DB(); err == nil {
		conn.Close()
	}
}

func userVersion(t *testing.T, g *gorm.DB) int {
	t.Helper()
	var v int
	if err := g.Raw("PRAGMA user_version").Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

func stepCount(t *testing.T) int {
	t.Helper()
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

var (
	space = regexp.MustCompile(`\s+`)
	check = regexp.MustCompile("CONSTRAINT `[^`]+` CHECK ")
)

// describe lists a database's schema as lines that do not depend on the
// order of columns or the layout of its SQL.
func describe(t *testing.T, g *gorm.DB) []string {
	t.Helper()
	var objects []struct{ Type, Name, SQL string }
	// The message search index is no row of gorm's; TestSearchFindsMessagesBySubstring covers it.
	if err := g.Raw("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'items_search%'").Scan(&objects).Error; err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, o := range objects {
		if o.Type == "index" {
			lines = append(lines, "index "+space.ReplaceAllString(o.SQL, " "))
			continue
		}
		var columns []struct {
			Name, Type string
			NotNull    bool
			Dflt       *string `gorm:"column:dflt_value"`
			PK         int
		}
		if err := g.Raw(fmt.Sprintf("PRAGMA table_info(`%s`)", o.Name)).Scan(&columns).Error; err != nil {
			t.Fatal(err)
		}
		for _, c := range columns {
			dflt := "-"
			if c.Dflt != nil {
				dflt = *c.Dflt
			}
			lines = append(lines, fmt.Sprintf("column %s.%s %s notnull=%v default=%s pk=%d", o.Name, c.Name, c.Type, c.NotNull, dflt, c.PK))
		}
		var keys []struct {
			Table, From, To string
			OnDelete        string `gorm:"column:on_delete"`
		}
		if err := g.Raw(fmt.Sprintf("PRAGMA foreign_key_list(`%s`)", o.Name)).Scan(&keys).Error; err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("key %s.%s -> %s.%s on delete %s", o.Name, k.From, k.Table, k.To, k.OnDelete))
		}
		for _, at := range check.FindAllStringIndex(o.SQL, -1) {
			lines = append(lines, fmt.Sprintf("check %s %s", o.Name, space.ReplaceAllString(balanced(o.SQL[at[0]:]), " ")))
		}
	}
	slices.Sort(lines)
	return lines
}

// balanced is s up to the parenthesis that closes its first one.
func balanced(s string) string {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

func diff(got, want []string) []string {
	var out []string
	for _, l := range got {
		if !slices.Contains(want, l) {
			out = append(out, "the migrations make, the rows do not: "+l)
		}
	}
	for _, l := range want {
		if !slices.Contains(got, l) {
			out = append(out, "the rows make, the migrations do not: "+l)
		}
	}
	return out
}

func TestTurnsFromBeforeRolesTakeTheRoleOfTheirThread(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := sqlitedb.Migrate(old, all[:8], 0); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	model := llm.Model{Name: "m", Connection: "c"}
	create := func(rows ...any) {
		t.Helper()
		for _, row := range rows {
			if err := old.Omit("Agent", "TurnSeq", "LaunchItemID").Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	// The users step made the default user.
	create(&ThreadRow{UserID: DefaultUser, ID: "main", Title: "main", CreatedAt: at, UpdatedAt: at},
		&TurnRow{ID: "asked", ThreadID: "main", Seq: 1, Status: TurnWaitingOnAgent, Model: model, CreatedAt: at, StartedAt: at})
	for i, tool := range []string{"Agent", "BrowserAgent"} {
		call, child := fmt.Sprintf("call%d", i), fmt.Sprintf("child%d", i)
		create(&ItemRow{ID: call, ThreadID: "main", TurnID: "asked", Seq: i + 1, Type: ItemToolCall, Status: ItemInProgress, Tool: tool, StartedAt: at},
			&ThreadRow{UserID: DefaultUser, ID: child, Title: child, ParentThreadID: "main", ParentItemID: Ref(call), CreatedAt: at, UpdatedAt: at},
			&TurnRow{ID: "of" + child, ThreadID: child, Seq: 1, Status: TurnInProgress, Model: model, CreatedAt: at, StartedAt: at})
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, want := range map[string]string{"asked": "full-main-agent", "ofchild0": "general-sub-agent", "ofchild1": "browser-sub-agent"} {
		turn, err := db.Turn(id)
		if err != nil || turn.Agent != want {
			t.Errorf("turn %s runs as %q (%v), want %q", id, turn.Agent, err, want)
		}
	}
}

// A call that waited on its subagent, from before handles and before reports came back as items, completes as a launch
// naming that subagent, and its turn, with no subagent of its running, is ready again.
func TestASubagentWaitFromBeforeCompletesAsALaunch(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := sqlitedb.Migrate(old, all[:9], 0); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	for _, row := range []any{
		&ThreadRow{UserID: DefaultUser, ID: "main", Title: "main", CreatedAt: at, UpdatedAt: at},
		&TurnRow{ID: "asked", ThreadID: "main", Seq: 1, Status: TurnWaitingOnAgent, Model: llm.Model{Name: "m", Connection: "c"}, Agent: "full-main-agent", CreatedAt: at, StartedAt: at},
		&ItemRow{ID: "call", ThreadID: "main", TurnID: "asked", Seq: 1, Type: ItemToolCall, Status: ItemInProgress, Tool: "Agent", StartedAt: at},
	} {
		if err := old.Omit("TurnSeq", "LaunchItemID").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Exec("UPDATE items SET await = ? WHERE id = 'call'", `{"by":"agent","request":{"prompt":"p"},"threadId":"child"}`).Error; err != nil {
		t.Fatal(err)
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	call, err := db.Item("call")
	if err != nil || call.Status != ItemCompleted || call.Await != nil || call.Result == nil || !strings.Contains(string(call.Result.Output), `"subagent":"child"`) {
		t.Errorf("the wait after the steps: %+v (%v)", call, err)
	}
	if turn, err := db.Turn("asked"); err != nil || turn.Status != TurnReady {
		t.Errorf("the turn that waited is %s (%v), want ready", turn.Status, err)
	}
}

func TestAQueuedTurnFromBeforeWaitsReadyWithItsMessage(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := sqlitedb.Migrate(old, all[:13], 0); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO threads(id, user_id, title, created_at, updated_at) VALUES ('t', 'default', 't', '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at)
			VALUES ('running', 't', 1, 'inProgress', '{"name":"m","connection":"c"}', 0, 0, 0, 0, '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO items(id, thread_id, turn_id, seq, type, status, text, started_at) VALUES ('ask', 't', 'running', 1, 'userMessage', 'completed', 'one', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at, input)
			VALUES ('waiting', 't', 2, 'queued', '{"name":"m","connection":"c"}', 0, 0, 0, 0, '2026-11-01 08:01:00+00:00', '2026-11-01 08:01:00+00:00', 'two')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at, input)
			VALUES ('last', 't', 3, 'queued', '{"name":"m","connection":"c"}', 0, 0, 0, 0, '2026-11-01 08:02:00+00:00', '2026-11-01 08:02:00+00:00', 'three')`,
	} {
		if err := old.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	view, err := db.Thread("t")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range view.Items {
		got = append(got, fmt.Sprintf("%s/%d/%s", it.TurnID, it.TurnSeq, it.Text))
	}
	if want := []string{"running/1/one", "waiting/2/two", "last/3/three"}; !slices.Equal(got, want) {
		t.Errorf("items after the step %v, want %v", got, want)
	}
	for _, turn := range view.Turns[1:] {
		if turn.Status != TurnReady {
			t.Errorf("turn %s is %s after the step, want ready", turn.ID, turn.Status)
		}
	}
	if current, err := db.ActiveTurn("t"); err != nil || current.ID != "running" {
		t.Errorf("current turn %s (%v), want the one in progress", current.ID, err)
	}
}

// A turn that waited on background calls is ready again with its wait failed, a background call's result stays as a
// notice with what it returned, and the background calls go.
func TestBackgroundCallsFromBeforeEndWithTheirStep(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := sqlitedb.Migrate(old, all[:16], 0); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO threads(id, user_id, title, created_at, updated_at) VALUES ('t', 'default', 't', '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at)
			VALUES ('waits', 't', 1, 'waitingOnBackground', '{"name":"m","connection":"c"}', 0, 0, 0, 0, '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO items(id, thread_id, turn_id, turn_seq, seq, type, status, tool, text, result, background_id, started_at)
			VALUES ('came', 't', 'waits', 1, 1, 'backgroundResult', 'completed', 'web_fetch', 'completed', '{"output":"# Welcome"}', 'b', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO items(id, thread_id, turn_id, turn_seq, seq, type, status, tool, await, started_at)
			VALUES ('wait', 't', 'waits', 1, 2, 'toolCall', 'inProgress', 'wait_for_background', '{"by":"background","request":{}}', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO background_calls(id, thread_id, item_id, tool, status, started_at) VALUES ('b', 't', 'call', 'web_fetch', 'running', '2026-11-01 08:00:00+00:00')`,
	} {
		if err := old.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if turn, err := db.Turn("waits"); err != nil || turn.Status != TurnReady {
		t.Errorf("the turn that waited is %s (%v), want ready", turn.Status, err)
	}
	if wait, err := db.Item("wait"); err != nil || wait.Status != ItemFailed || wait.Await != nil {
		t.Errorf("the wait after the step: %+v (%v)", wait, err)
	}
	if came, err := db.Item("came"); err != nil || came.Type != ItemError || !strings.Contains(came.Text, "web_fetch ended: completed") || !strings.Contains(came.Text, "# Welcome") {
		t.Errorf("the result after the step: %+v (%v)", came, err)
	}
	var tables int64
	if err := db.gorm.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'background_calls'").Scan(&tables).Error; err != nil || tables != 0 {
		t.Errorf("background_calls is still there: %d (%v)", tables, err)
	}
}

// A report from before it named its launch names the latest call of its call id before it, so two launches a provider
// gave the same call id in different responses each have their own report.
func TestAReportFromBeforeNamesItsLaunch(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := shardSteps()
	if err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := sqlitedb.Migrate(old, all[:17], 0); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO threads(id, user_id, title, created_at, updated_at) VALUES ('t', 'default', 't', '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, agent, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at)
			VALUES ('u', 't', 1, 'completed', '{"name":"m","connection":"c"}', 'full-main-agent', 0, 0, 0, 0, '2026-11-01 08:00:00+00:00', '2026-11-01 08:00:00+00:00')`,
	} {
		if err := old.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"first", "report1", "second", "report2"} {
		typ := "toolCall"
		if i%2 == 1 {
			typ = "agentReport"
		}
		if err := old.Exec(`INSERT INTO items(id, thread_id, turn_id, turn_seq, seq, type, status, call_id, tool, started_at)
			VALUES (?, 't', 'u', 1, ?, ?, 'completed', 'call_0', 'general-sub-agent', '2026-11-01 08:00:00+00:00')`, id, i+1, typ).Error; err != nil {
			t.Fatal(err)
		}
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for report, launch := range map[string]string{"report1": "first", "report2": "second"} {
		if it, err := db.Item(report); err != nil || it.LaunchItemID != launch {
			t.Errorf("%s names %q (%v), want %s", report, it.LaunchItemID, err, launch)
		}
	}
}

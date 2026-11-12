package repo

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sharedShard is a shard with two users, each with a workspace at the same path, a thread and a message that say the
// same thing, a scheduled task and a memory on the same topic.
func sharedShard(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
		if err := db.saveWorkspace(WorkspaceRow{ID: "w-" + user, UserID: user, Name: "app", RootPath: "/srv/app", CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		thread := "t-" + user
		if err := db.CreateThread(ThreadRow{ID: thread, UserID: user, Title: "shared words", WorkspaceID: Ref("w-" + user), CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := db.createTurn(TurnRow{ID: "turn-" + user, ThreadID: thread, Seq: 1, Status: TurnWaitingOnUserInput, Model: llm.Model{Name: "m", Connection: "c"}, CreatedAt: at, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.RecordItem(thread, ItemRow{ID: "i-" + user, TurnID: "turn-" + user, Type: ItemUserMessage, Status: ItemCompleted, Text: "shared words", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateCronJob(CronJobRow{ID: "j-" + user, ThreadID: thread, Name: "shared words", Spec: "@daily", Prompt: "go", NextRunAt: at, CreatedAt: at}, 10); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveMemory(MemoryRow{ID: "m-" + user, UserID: user, Scope: MemoryGlobal, Topic: "editor", Kind: "fact", Text: user, Status: MemoryActive, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestUsersOnAShardSeeOnlyTheirOwn(t *testing.T) {
	db := sharedShard(t)
	for user, other := range map[string]string{"a": "b", "b": "a"} {
		if _, err := db.ThreadOf(user, "t-"+other); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s reads the thread of %s: %v", user, other, err)
		}
		if _, err := db.WorkspaceOf(user, "w-"+other); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s reads the workspace of %s: %v", user, other, err)
		}
		if _, err := db.Memory(user, "m-"+other); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s reads the memory of %s: %v", user, other, err)
		}
		if _, err := db.UpdateWorkspace(user, "w-"+other, func(w WorkspaceRow) (WorkspaceRow, error) { return w, nil }); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s changes the workspace of %s: %v", user, other, err)
		}

		threads, _, err := db.ThreadRows(user, ThreadFilter{Limit: 10})
		if err != nil || len(threads) != 1 || threads[0].ID != "t-"+user {
			t.Errorf("threads of %s: %+v, %v", user, threads, err)
		}
		workspaces, err := db.Workspaces(user)
		if err != nil || len(workspaces) != 1 || workspaces[0].ID != "w-"+user {
			t.Errorf("workspaces of %s: %+v, %v", user, workspaces, err)
		}
		memories, err := db.AllMemories(user)
		if err != nil || len(memories) != 1 || memories[0].ID != "m-"+user {
			t.Errorf("memories of %s: %+v, %v", user, memories, err)
		}
		if memories, err = db.Memories(user, MemoryGeneral, "", true); err != nil || len(memories) != 1 || memories[0].ID != "m-"+user {
			t.Errorf("global memories of %s: %+v, %v", user, memories, err)
		}
		jobs, err := db.CronJobViews(user, "")
		if err != nil || len(jobs) != 1 || jobs[0].ID != "j-"+user {
			t.Errorf("tasks of %s: %+v, %v", user, jobs, err)
		}
		if jobs, err = db.CronJobViews(user, "t-"+other); err != nil || len(jobs) != 0 {
			t.Errorf("%s lists the tasks of %s: %+v, %v", user, other, jobs, err)
		}
		waiting, err := db.ThreadsWaitingOnUser(user)
		if err != nil || !slices.Equal(waiting, []string{"t-" + user}) {
			t.Errorf("waiting on %s: %v, %v", user, waiting, err)
		}
		found, err := db.Search(user, []string{"shared"}, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(found.Threads) != 1 || found.Threads[0].ID != "t-"+user {
			t.Errorf("%s found threads %+v", user, found.Threads)
		}
		if len(found.Messages) != 1 || found.Messages[0].ItemID != "i-"+user {
			t.Errorf("%s found messages %+v", user, found.Messages)
		}
		if len(found.Tasks) != 1 || found.Tasks[0].ID != "j-"+user {
			t.Errorf("%s found tasks %+v", user, found.Tasks)
		}
	}
}

func TestSettingsAndMemoryStateAreEachUsers(t *testing.T) {
	db := sharedShard(t)
	a, err := db.Settings("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Settings("b")
	if err != nil {
		t.Fatal(err)
	}
	if a.RedactionKey == "" || a.RedactionKey == b.RedactionKey {
		t.Errorf("redaction keys %q and %q", a.RedactionKey, b.RedactionKey)
	}
	if _, err := db.SetRedaction("a", false); err != nil {
		t.Fatal(err)
	}
	if b, err = db.Settings("b"); err != nil || !b.Redaction {
		t.Errorf("turning redaction off for a reached b: %+v, %v", b, err)
	}
	if err := db.SetMemoryFailure("a", MemoryFailure{Code: MemoryModelRequest, Error: "from a"}); err != nil {
		t.Fatal(err)
	}
	status, err := db.MemoryStatus("b")
	if err != nil || status.LastFailure != nil || status.Active != 1 {
		t.Errorf("status of b: %+v, %v", status, err)
	}

	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if err := db.gorm.Model(&TurnRow{}).Where("id = ?", "turn-a").Updates(map[string]any{"status": TurnCompleted, "completed_at": at}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.NextEndedTurn("b", MemoryCursor{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("b's memory reads the turn of a: %v", err)
	}
	if turn, err := db.NextEndedTurn("a", MemoryCursor{}); err != nil || turn.ID != "turn-a" {
		t.Errorf("a's next ended turn: %+v, %v", turn, err)
	}
	if status, err = db.MemoryStatus("b"); err != nil || status.Pending != 0 {
		t.Errorf("b's pending memory counts a: %+v, %v", status, err)
	}
}

func TestARowWithoutAUserIsRefused(t *testing.T) {
	db := sharedShard(t)
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{ID: "nobody", CreatedAt: at, UpdatedAt: at}); err == nil {
		t.Error("a thread with no user was stored")
	}
	if err := db.saveWorkspace(WorkspaceRow{ID: "nobody", Name: "x", RootPath: "/x", CreatedAt: at, UpdatedAt: at}); err == nil {
		t.Error("a workspace with no user was stored")
	}
	if err := db.SaveMemory(MemoryRow{ID: "nobody", Scope: MemoryGlobal, Topic: "x", Kind: "fact", Text: "x", Status: MemoryActive, CreatedAt: at, UpdatedAt: at}); err == nil {
		t.Error("a memory with no user was stored")
	}
	if err := db.EnsureUser(""); err == nil {
		t.Error("a user with no id was made")
	}
}

func TestEnsureUserKeepsAUserWhoExists(t *testing.T) {
	db := sharedShard(t)
	before, err := db.Settings("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureUser("a"); err != nil {
		t.Fatal(err)
	}
	after, err := db.Settings("a")
	if err != nil || after.RedactionKey != before.RedactionKey {
		t.Errorf("the key changed from %q to %q: %v", before.RedactionKey, after.RedactionKey, err)
	}
	users, err := db.Users()
	if err != nil || !slices.Equal(users, []string{"a", "b", DefaultUser}) {
		t.Errorf("users: %v, %v", users, err)
	}
}

// A database from before users has its rows, with their turns and items, under the default user.
func TestRowsFromBeforeUsersBelongToTheDefaultUser(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := gorm.Open(sqlite.Open("file:"+path+"?_foreign_keys=off"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	all, err := steps(shardMigrations)
	if err != nil {
		t.Fatal(err)
	}
	const before = 4
	for _, step := range all[:before] {
		if err := old.Exec(step).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		"PRAGMA user_version = 4",
		`INSERT INTO workspaces(id, name, root_path, created_at, updated_at) VALUES ('w', 'app', '/srv/app', '2026-10-30 08:00:00+00:00', '2026-10-30 08:00:00+00:00')`,
		`INSERT INTO threads(id, workspace_id, title, created_at, updated_at) VALUES ('t', 'w', 'kept', '2026-10-30 08:00:00+00:00', '2026-10-30 08:00:00+00:00')`,
		`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at) VALUES ('turn', 't', 1, 'waitingOnUserInput', '{}', 0, 0, 0, 0, '2026-10-30 08:00:00+00:00', '2026-10-30 08:00:00+00:00')`,
		`INSERT INTO items(id, thread_id, turn_id, seq, type, status, text, started_at) VALUES ('i', 't', 'turn', 1, 'userMessage', 'completed', 'kept words', '2026-10-30 08:00:00+00:00')`,
		`INSERT INTO memories(id, scope, topic, kind, text, status, user_edited, sources, created_at, updated_at) VALUES ('m', 'global', 'editor', 'fact', 'vim', 'active', 0, '[]', '2026-10-30 08:00:00+00:00', '2026-10-30 08:00:00+00:00')`,
		`INSERT INTO settings(id, redaction, redaction_key) VALUES (1, false, 'the key')`,
		`INSERT INTO memory_state(id, cursor_id, skipped) VALUES (1, 'turn', 7)`,
	} {
		if err := old.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	conn, err := old.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if thread, err := db.ThreadOf(DefaultUser, "t"); err != nil || thread.WorkspaceID != "w" {
		t.Errorf("the thread: %+v, %v", thread, err)
	}
	if _, err := db.WorkspaceOf(DefaultUser, "w"); err != nil {
		t.Error(err)
	}
	if _, err := db.Memory(DefaultUser, "m"); err != nil {
		t.Error(err)
	}
	if waiting, err := db.ThreadsWaitingOnUser(DefaultUser); err != nil || !slices.Equal(waiting, []string{"t"}) {
		t.Errorf("waiting: %v, %v", waiting, err)
	}
	if found, err := db.Search(DefaultUser, []string{"kept words"}, "", 10); err != nil || len(found.Messages) != 1 {
		t.Errorf("the message: %+v, %v", found.Messages, err)
	}
	settings, err := db.Settings(DefaultUser)
	if err != nil || settings.Redaction || settings.RedactionKey != "the key" {
		t.Errorf("settings: %+v, %v", settings, err)
	}
	state, err := db.MemoryState(DefaultUser)
	if err != nil || state.Cursor.ID != "turn" || state.Skipped != 7 {
		t.Errorf("memory state: %+v, %v", state, err)
	}
}

func TestOneProcessHoldsTheShard(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	claim := func(owner string, now time.Time, want bool) {
		t.Helper()
		if got, err := db.ClaimShard(owner, now, time.Minute); err != nil || got != want {
			t.Fatalf("%s claiming at %v: %v, %v", owner, now, got, err)
		}
	}
	claim("first", at, true)
	claim("second", at.Add(59*time.Second), false)
	claim("first", at.Add(59*time.Second), true)
	claim("second", at.Add(time.Minute), false)
	claim("second", at.Add(2*time.Minute), true)
	claim("first", at.Add(2*time.Minute), false)
	if err := db.ReleaseShard("first"); err != nil {
		t.Fatal(err)
	}
	claim("first", at.Add(2*time.Minute), false)
	if err := db.ReleaseShard("second"); err != nil {
		t.Fatal(err)
	}
	claim("first", at.Add(2*time.Minute), true)
}

func TestAFencedHandleStopsWritingOnceTheShardIsTaken(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now()
	if held, err := db.ClaimShard("first", at, time.Minute); err != nil || !held {
		t.Fatal(held, err)
	}
	db.Fence("first")
	write := func() error { return db.Tx(func(tx *DB) error { return tx.EnsureUser("a") }) }
	if err := write(); err != nil {
		t.Fatal(err)
	}
	if held, err := db.ClaimShard("second", at.Add(time.Hour), time.Minute); err != nil || !held {
		t.Fatal(held, err)
	}
	if err := write(); !errors.Is(err, ErrShardLost) {
		t.Errorf("writing after the shard was taken: %v", err)
	}
}

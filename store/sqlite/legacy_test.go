package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/store"
)

func oldDatabase(t *testing.T, home string) {
	t.Helper()
	g, err := sqlitedb.Open(filepath.Join(home, "data", legacyFile))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlitedb.Close(g)
	if err := sqlitedb.Migrate(g, legacyMigrations, "legacy"); err != nil {
		t.Fatal(err)
	}
	at := func(minute int) time.Time { return time.Date(2026, 11, 1, 9, minute, 0, 0, time.UTC) }
	model := `{"name":"m","connection":"main"}`
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT OR IGNORE INTO users(id, created_at) VALUES ('default', ?)", []any{at(0)}},
		{"INSERT OR REPLACE INTO settings(user_id, redaction, redaction_key, permissions, memory) VALUES ('default', 1, 'the-key', '{\"approvalPolicy\":\"on-request\"}', 1)", nil},
		{"UPDATE environments SET id = 'env' WHERE user_id = 'default' AND kind = 'local'", nil},
		{"INSERT INTO workspaces(id, user_id, name, root_path, settings, created_at, updated_at) VALUES ('ws', 'default', 'notes', '/notes', '{}', ?, ?)", []any{at(0), at(0)}},
		{`INSERT INTO threads(id, user_id, workspace_id, environment_id, title, settings, created_at, updated_at, replied_at, read_at)
			VALUES ('th', 'default', 'ws', 'env', 'plans', '{}', ?, ?, ?, ?)`, []any{at(1), at(30), at(20), at(25)}},
		{`INSERT INTO threads(id, user_id, workspace_id, environment_id, title, settings, parent_thread_id, parent_item_id, created_at, updated_at)
			VALUES ('child', 'default', 'ws', 'env', 'look it up', '{}', 'th', 'launch', ?, ?)`, []any{at(5), at(9)}},
		{`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at, completed_at, agent)
			VALUES ('t1', 'th', 1, 'completed', ?, 0, 0, 0, 0, ?, ?, ?, 'full-main-agent')`, []any{model, at(2), at(2), at(20)}},
		{`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at, agent)
			VALUES ('t2', 'th', 2, 'waitingOnApproval', ?, 0, 0, 0, 0, ?, ?, 'full-main-agent')`, []any{model, at(21), at(21)}},
		{`INSERT INTO turns(id, thread_id, seq, status, model, input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, created_at, started_at, completed_at, agent)
			VALUES ('c1', 'child', 1, 'completed', ?, 0, 0, 0, 0, ?, ?, ?, 'general-sub-agent')`, []any{model, at(5), at(5), at(9)}},
	} {
		if err := g.Exec(q.sql, q.args...).Error; err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	item := func(id, thread, turn string, seq int, kind, status string, fields map[string]any) {
		t.Helper()
		row := map[string]any{"id": id, "thread_id": thread, "turn_id": turn, "turn_seq": 1, "seq": seq, "type": kind, "status": status,
			"started_at": at(seq), "completed_at": at(seq)}
		if turn == "t2" {
			row["turn_seq"] = 2
		}
		for k, v := range fields {
			row[k] = v
		}
		if err := g.Table("items").Create(row).Error; err != nil {
			t.Fatalf("item %s: %v", id, err)
		}
	}
	item("m1", "th", "t1", 2, "userMessage", "completed", map[string]any{"text": "plan my week"})
	item("r1", "th", "t1", 3, "reasoning", "completed", map[string]any{"text": "thinking", "response_id": "resp1",
		"usage": `{"inputTokens":10,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":5}`, "context_stats": `{"budgetTokens":1000,"estimatedTokens":120}`})
	item("call", "th", "t1", 4, "toolCall", "completed", map[string]any{"tool": "run_command", "call_id": "x", "response_id": "resp1",
		"arguments": `{"command":"ls"}`, "approval": `{"id":"a1","requestedAt":"2026-11-01T09:04:00Z","content":"ls","decision":"accept","decidedBy":"default"}`,
		"result": `{"output":"notes.md"}`})
	item("ask", "th", "t1", 5, "toolCall", "completed", map[string]any{"tool": "request_user_input", "response_id": "resp2",
		"await": `{"by":"user","request":{"questions":[{"id":"day","header":"Day","question":"Which day?"}]}}`, "result": `{"output":{"day":{"answers":["Monday"]}}}`})
	item("launch", "th", "t1", 6, "toolCall", "completed", map[string]any{"tool": "Agent", "response_id": "resp2",
		"await": `{"by":"agent","request":{}}`, "result": `{"output":"the child's answer"}`})
	item("a1", "th", "t1", 7, "agentMessage", "completed", map[string]any{"text": "Monday it is.", "response_id": "resp3"})
	item("m2", "th", "t2", 8, "userMessage", "completed", map[string]any{"text": "clean up"})
	item("rm", "th", "t2", 9, "toolCall", "pending", map[string]any{"tool": "run_command", "response_id": "resp4", "arguments": `{"command":"rm -rf x"}`,
		"approval": `{"id":"a2","requestedAt":"2026-11-01T09:09:00Z","content":"rm -rf x"}`})
	item("cm", "child", "c1", 1, "userMessage", "completed", map[string]any{"text": "look it up"})
	item("ca", "child", "c1", 2, "agentMessage", "completed", map[string]any{"text": "the child's answer", "response_id": "cr"})
	for _, q := range []string{
		`INSERT INTO memories(id, user_id, scope, workspace_id, origin_workspace_id, topic, kind, text, status, user_edited, sources, created_at, updated_at)
			VALUES ('n1', 'default', 'global', '', '', 'week', 'preference', 'Plans on Monday.', 'active', 0, '[{"table":"items","id":"m1"}]', '2026-11-01', '2026-11-01')`,
		`INSERT INTO cron_jobs(id, thread_id, name, spec, prompt, times, runs, paused, next_run_at, created_at)
			VALUES ('job', 'th', 'weekly', '@weekly', 'plan the week', NULL, 3, 0, '2026-11-08 09:00:00', '2026-11-01 09:00:00')`,
	} {
		if err := g.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestAnOldDatabaseIsBroughtInOnce(t *testing.T) {
	home := t.TempDir()
	oldDatabase(t, home)
	oldPath := filepath.Join(home, "data", legacyFile)
	before, _ := os.ReadFile(oldPath)
	s := open(t, home)
	var v conversation.View
	var th store.Thread
	var note memory.Memory
	var state memory.State
	var jobs int
	err := s.Read(context.Background(), func(r store.Reader) (err error) {
		if v, err = r.View("th"); err != nil {
			return err
		}
		if th, err = r.Thread("th"); err != nil {
			return err
		}
		if note, err = r.Memory("default", "n1"); err != nil {
			return err
		}
		if state, err = r.MemoryState("default"); err != nil {
			return err
		}
		all, err := r.CronJobs("default", "")
		jobs = len(all)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Turns) != 2 || v.Turns[0].Ended.Status != conversation.Completed || v.Turns[1].Ended.Status != conversation.Interrupted ||
		v.Turns[1].Ended.Reason != upgraded {
		t.Fatalf("the turns %+v", v.Turns)
	}
	call, _ := v.ItemOf("call")
	ask, _ := v.ItemOf("ask")
	launch, _ := v.ItemOf("launch")
	rm, _ := v.ItemOf("rm")
	switch {
	case call.Status != conversation.Done || call.Approval.Decision != conversation.Accept || string(call.Result.Output) != `"notes.md"`:
		t.Errorf("the approved call %+v", call)
	case ask.Status != conversation.Done || ask.Input == nil || !ask.Input.Answered:
		t.Errorf("the question %+v", ask)
	case launch.Child != "child" || launch.Status != conversation.Done:
		t.Errorf("the launch %+v", launch)
	case rm.Status != conversation.Declined || rm.Error != upgraded:
		t.Errorf("the call left waiting %+v", rm)
	}
	if len(v.Responses) == 0 || v.Responses[0].Usage.InputTokens != 10 || v.Responses[0].Stats.BudgetTokens != 1000 {
		t.Errorf("the response %+v", v.Responses)
	}
	if !th.Updated.Equal(time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)) || th.Workspace != "ws" || th.Status != conversation.ThreadIdle {
		t.Errorf("the conversation %+v", th)
	}
	if len(note.Sources) != 1 || note.Sources[0] != (memory.Source{Thread: "th", Item: "m1"}) || jobs != 1 || state.Cursor.Seq == 0 {
		t.Errorf("note %+v, %d tasks, state %+v", note, jobs, state)
	}
	settings := read(t, s, func(r store.Reader) (string, error) { st, err := r.Settings("default"); return st.RedactionKey, err })
	if settings != "the-key" {
		t.Errorf("the redaction key %q", settings)
	}
	s.Close()
	if after, _ := os.ReadFile(oldPath); string(after) != string(before) {
		t.Error("the old database changed")
	}
	again := open(t, home)
	if n := read(t, again, func(r store.Reader) ([]string, error) { return r.Users() }); len(n) != 1 {
		t.Errorf("opened again: %v", n)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "ollupi-import.sqlite")); !os.IsNotExist(err) {
		t.Errorf("the copy was left: %v", err)
	}
}

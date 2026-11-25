package sqlite

import (
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/store"
)

// Step 0026 renamed the roles of the subagents that are gone; step 0027
// renames the calls that launched them, so a launch kept in a conversation
// names a tool there is: general-sub-agent, or explore-sub-agent for the
// codebase and research subagents.
func TestOldSubagentLaunchesNameTheirNewTool(t *testing.T) {
	home := t.TempDir()
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries[:26] {
		body, err := fs.ReadFile(migrations, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before["steps/"+e.Name()] = &fstest.MapFile{Data: body}
	}
	g, err := sqlitedb.Open(filepath.Join(home, "data", "store.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.Migrate(g, before, "steps"); err != nil {
		t.Fatal(err)
	}
	call := func(id, tool string) string {
		return `('th', '` + id + `', 't', 1, 1, 'toolCall', 'completed', '2026-11-03 00:00:00', '{"ID":"` + id + `","Kind":"toolCall","Call":{"Call":"c","Tool":"` + tool + `","Server":"subagents"}}')`
	}
	for _, q := range []string{
		`INSERT INTO users(id, name, created_at) VALUES ('u', 'Me', '2026-11-01 00:00:00')`,
		`INSERT INTO environments(id, user_id, name, provider, created_at, updated_at) VALUES ('here', 'u', 'This computer', 'local', '2026-11-02 00:00:00', '2026-11-02 00:00:00')`,
		`INSERT INTO workspaces(id, user_id, environment_id, name, root, created_at, updated_at) VALUES ('w', 'u', 'here', 'app', '/srv/app', '2026-11-03 00:00:00', '2026-11-03 00:00:00')`,
		`INSERT INTO threads(id, user_id, workspace_id, created_at, updated_at) VALUES ('th', 'u', 'w', '2026-11-03 00:00:00', '2026-11-03 00:00:00')`,
		`INSERT INTO items(thread_id, id, turn_id, seq, updated, kind, status, at, body) VALUES ` +
			call("design", "design-sub-agent") + `, ` + call("research", "research-sub-agent") + `, ` + call("general", "general-sub-agent") + `, ` + call("read", "read_file"),
	} {
		if err := g.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlitedb.Close(g)

	s := open(t, home)
	ids := func(tool string) []string {
		calls := read(t, s, func(r store.Reader) ([]conversation.Item, error) { return r.Calls("u", "w", tool) })
		out := []string{}
		for _, c := range calls {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids("general-sub-agent"); len(got) != 2 || got[0] != "design" && got[1] != "design" {
		t.Errorf("general-sub-agent launches: %v", got)
	}
	if got := ids("explore-sub-agent"); len(got) != 1 || got[0] != "research" {
		t.Errorf("explore-sub-agent launches: %v", got)
	}
	if got := ids("read_file"); len(got) != 1 {
		t.Errorf("another call was renamed: %v", got)
	}
}

package sqlite

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/store"
)

// Before step 0023 an environment was this machine ('local') or a cloud
// machine named and never made ('machine'). The step makes the first the
// local provider's and moves the conversations of the second to it; every
// workspace becomes a folder of its user's first environment.
func TestEnvironmentsBecomeSandboxesOfAProvider(t *testing.T) {
	home := t.TempDir()
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries[:22] {
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
	for _, q := range []string{
		`INSERT INTO users(id, name, created_at) VALUES ('u', 'Me', '2026-11-01 00:00:00')`,
		`INSERT INTO environments(id, user_id, name, kind, created_at, updated_at) VALUES
			('cloud', 'u', 'Cloud', 'machine', '2026-11-01 00:00:00', '2026-11-01 00:00:00'),
			('here', 'u', 'This computer', 'local', '2026-11-02 00:00:00', '2026-11-02 00:00:00')`,
		`INSERT INTO workspaces(id, user_id, name, root, created_at, updated_at) VALUES ('w', 'u', 'app', '/srv/app', '2026-11-03 00:00:00', '2026-11-03 00:00:00')`,
		`INSERT INTO threads(id, user_id, workspace_id, environment_id, created_at, updated_at) VALUES
			('in-cloud', 'u', NULL, 'cloud', '2026-11-03 00:00:00', '2026-11-03 00:00:00'),
			('in-app', 'u', 'w', 'here', '2026-11-03 00:00:00', '2026-11-03 00:00:00')`,
	} {
		if err := g.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlitedb.Close(g)

	s := open(t, home)
	envs := read(t, s, func(r store.Reader) ([]account.Environment, error) { return r.Environments("u") })
	if len(envs) != 1 || envs[0].ID != "here" || envs[0].Provider != "local" {
		t.Fatalf("environments %+v", envs)
	}
	th := read(t, s, func(r store.Reader) (store.Thread, error) { return r.Thread("in-cloud") })
	scratch := read(t, s, func(r store.Reader) (account.Workspace, error) { return r.Scratch("u", "here") })
	if th.Workspace != scratch.ID || scratch.Root != "" || !scratch.Scratch() {
		t.Errorf("a conversation of the cloud machine is in %q, not the scratch workspace %+v", th.Workspace, scratch)
	}
	w := read(t, s, func(r store.Reader) (account.Workspace, error) { return r.Workspace("u", "w") })
	if w.Environment != "here" || w.Root != "/srv/app" {
		t.Errorf("the workspace %+v", w)
	}
	inUse := read(t, s, func(r store.Reader) (bool, error) { return r.EnvironmentInUse("u", "here") })
	if !inUse {
		t.Error("the environment of a workspace is not in use")
	}
	err = s.Update(context.Background(), func(tx store.Tx) error { return tx.DeleteEnvironment("u", "here") })
	if err == nil {
		t.Error("an environment with a workspace in it was deleted")
	}
}

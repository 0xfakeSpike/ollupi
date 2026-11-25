package sandbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEveryEnvironmentOfThisMachineIsAFolderOfItsOwn(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Dir: dir, Config: Config{Mode: string(ModeFullAccess), Roots: []string{"/ignored"}}}
	ctx := context.Background()
	a, err := l.Open(ctx, Env{User: "u", ID: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Open(ctx, Env{User: "u", ID: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Roots(); len(got) != 1 || got[0] != filepath.Join(dir, "u", "e1") {
		t.Errorf("e1 is in %v", got)
	}
	if b.Roots()[0] == a.Roots()[0] {
		t.Error("two environments share a folder")
	}
	if st, err := a.Stat(ctx, a.Roots()[0]); err != nil || !st.IsDir() {
		t.Errorf("the folder was not made: %v", err)
	}
	for _, bad := range []Env{{User: "..", ID: "e"}, {User: "u", ID: "a/b"}, {User: "u"}} {
		if _, err := l.Open(ctx, bad); err == nil {
			t.Errorf("opened %+v", bad)
		}
	}
	if l.Check(nil) != nil || l.Check(json.RawMessage(`{}`)) != nil || l.Check(json.RawMessage(`{"region":"x"}`)) == nil {
		t.Error("this machine's settings are not none")
	}
	if l.Remove(ctx, Env{User: "u", ID: "e1"}) != nil {
		t.Error("removing an environment of this machine failed")
	}
	if st, err := a.Stat(ctx, a.Roots()[0]); err != nil || !st.IsDir() {
		t.Error("removing the environment took the user's files")
	}
}

func TestACommandWithFullAccessGetsTheEnvironmentButWhatIsHidden(t *testing.T) {
	t.Setenv("OLLUPI_GATEWAY_TOKEN", "secret")
	t.Setenv("MY_TOOL_KEY", "mine")
	box, err := New(Config{Mode: string(ModeFullAccess), Roots: []string{t.TempDir()}, HideEnv: []string{"OLLUPI_"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := box.Run(context.Background(), Command{Program: "env", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "MY_TOOL_KEY=mine") || strings.Contains(res.Stdout, "OLLUPI_GATEWAY_TOKEN") {
		t.Errorf("the command's environment:\n%s", res.Stdout)
	}
}

func TestADeploymentNamesTheFolderOfAnEnvironment(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(dir, "scratch")
	l := Local{Dir: filepath.Join(dir, "users"), Config: Config{Mode: string(ModeFullAccess)},
		Folder: func(_ context.Context, env Env) (string, error) {
			if env.ID == "first" {
				return named, nil
			}
			return "", nil
		}}
	ctx := context.Background()
	first, err := l.Open(ctx, Env{User: "u", ID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := l.Open(ctx, Env{User: "u", ID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Roots()[0] != named || other.Roots()[0] != filepath.Join(dir, "users", "u", "other") {
		t.Errorf("folders %v and %v", first.Roots(), other.Roots())
	}
}

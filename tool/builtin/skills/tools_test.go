package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

func setup(t *testing.T) (*Provider, *sqlite.Store, string) {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := sandbox.New(sandbox.Config{Mode: "full-access", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { box.Close() })
	return New(st, func(string) (sandbox.Box, error) { return box, nil }), st, box.Roots()[0]
}

func put(t *testing.T, st store.Store, p skill.Package) {
	t.Helper()
	if err := st.Update(context.Background(), func(tx store.Tx) error { return tx.PutSkill(p) }); err != nil {
		t.Fatal(err)
	}
}

func call(ts *Provider, role, name string, args map[string]any) (kit.Result, error) {
	return ts.Invoke(context.Background(), tool.Call{Caller: tool.Caller{User: "u", Role: role}, Tool: name, Arguments: args})
}

func TestACallReachesOnlyItsRolesSkills(t *testing.T) {
	ts, st, _ := setup(t)
	put(t, st, skill.Package{Name: "deploy", Description: "How to deploy.", Instructions: "Deploy.", Roles: []string{"full-main-agent"},
		Files: []skill.File{{Path: "docs/guide.md", Body: []byte("guide")}}})
	put(t, st, skill.Package{Name: "site-login", Description: "How to log in.", Instructions: "Log in.", Roles: []string{"browser-sub-agent"}})
	if _, err := call(ts, "full-main-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err != nil {
		t.Errorf("a role's own skill: %v", err)
	}
	if _, err := call(ts, "browser-sub-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err == nil {
		t.Error("the browser subagent activated a skill of the main agent")
	}
	if _, err := call(ts, "browser-sub-agent", toolReadResource, map[string]any{"skill": "deploy", "path": "docs/guide.md"}); err == nil {
		t.Error("the browser subagent read a resource of the main agent")
	}
	listed, err := call(ts, "browser-sub-agent", toolListSkills, map[string]any{})
	if got := listed.Output.(map[string]any)["skills"].([]skill.Skill); err != nil || len(got) != 1 || got[0].Name != "site-login" {
		t.Errorf("the browser subagent's list: %+v %v", listed, err)
	}
	page, err := call(ts, "full-main-agent", toolReadResource, map[string]any{"skill": "deploy", "path": "docs/guide.md"})
	if err != nil || page.Output.(skill.ResourcePage).Text != "guide" {
		t.Errorf("the main agent's guide: %+v %v", page, err)
	}
}

func TestActivatingASkillPutsItsFilesInTheSandboxOncePerVersion(t *testing.T) {
	ts, st, root := setup(t)
	deploy := skill.Package{Name: "deploy", Description: "How to deploy.", Instructions: "Deploy.", Roles: []string{"full-main-agent"},
		Files: []skill.File{{Path: "scripts/run.sh", Body: []byte("echo run"), Executable: true}}}
	put(t, st, deploy)
	activated, err := call(ts, "full-main-agent", toolActivateSkill, map[string]any{"name": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".ollupi", "skills", "deploy")
	if got := activated.Output.(map[string]any)["directory"]; got != dir {
		t.Errorf("the directory: %v", got)
	}
	script := filepath.Join(dir, "scripts", "run.sh")
	info, err := os.Stat(script)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the script: %v %v", info, err)
	}

	if err := os.WriteFile(script, []byte("changed in the sandbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ts, "full-main-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(script); string(body) != "changed in the sandbox" {
		t.Errorf("the same version was written again: %q", body)
	}

	deploy.Files[0].Body = []byte("echo run 2")
	put(t, st, deploy)
	if _, err := call(ts, "full-main-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(script); string(body) != "echo run 2" {
		t.Errorf("a new version was not written: %q", body)
	}
}

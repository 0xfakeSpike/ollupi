package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

func TestACallReachesOnlyItsRolesSkills(t *testing.T) {
	root := t.TempDir()
	for _, role := range []string{"full-main-agent", "browser-sub-agent"} {
		name := map[string]string{"full-main-agent": "deploy", "browser-sub-agent": "site-login"}[role]
		dir := filepath.Join(root, role, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: How to " + name + ".\n---\nDo " + name + " step by step.\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := New(root)
	call := func(role, name string, args map[string]any) (kit.Result, error) {
		return ts.Invoke(context.Background(), tool.Call{Caller: tool.Caller{Role: role}, Tool: name, Arguments: args})
	}
	if _, err := call("full-main-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err != nil {
		t.Errorf("a role's own skill: %v", err)
	}
	if _, err := call("browser-sub-agent", toolActivateSkill, map[string]any{"name": "deploy"}); err == nil {
		t.Error("the browser subagent activated a skill of the main agent")
	}
	listed, err := call("browser-sub-agent", toolListSkills, map[string]any{})
	if got := listed.Output.(map[string]any)["skills"].([]skill.Skill); err != nil || len(got) != 1 || got[0].Name != "site-login" {
		t.Errorf("the browser subagent's list: %+v %v", listed, err)
	}
	dir, err := call("browser-sub-agent", toolGetRootDir, map[string]any{})
	if err != nil || dir.Output.(map[string]any)["skills_root_dir"] != filepath.Join(root, "browser-sub-agent") {
		t.Errorf("the browser subagent's directory: %+v %v", dir, err)
	}
}

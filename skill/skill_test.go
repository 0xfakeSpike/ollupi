package skill

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAResourceStaysInsideItsSkill(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "skill")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{filepath.Join(root, "docs", "guide.md"): "guide", filepath.Join(base, "secret.txt"): "secret"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := readResource(root, "docs/guide.md"); err != nil || string(got) != "guide" {
		t.Errorf("a file in the skill: %q %v", got, err)
	}
	for _, bad := range []string{"../secret.txt", "docs/../../secret.txt", filepath.Join(base, "secret.txt"), "docs/link.md", "..", "."} {
		if got, err := readResource(root, bad); err == nil {
			t.Errorf("%s reached %q", bad, got)
		}
	}
}

func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: How to " + name + ".\n---\nDo " + name + " step by step.\n"
	if err := os.WriteFile(filepath.Join(dir, name, skillInstructionsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestARoleHasTheSkillsOfItsDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "full-main-agent"), "deploy")
	writeSkill(t, filepath.Join(root, "browser-sub-agent"), "site-login")
	names := func(role string) []string {
		t.Helper()
		own, err := Load(root, role)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range own {
			out = append(out, s.Name)
		}
		return out
	}
	if !slices.Equal(names("full-main-agent"), []string{"deploy"}) || !slices.Equal(names("browser-sub-agent"), []string{"site-login"}) || names("general-sub-agent") != nil {
		t.Errorf("full %q, browser %q, general %q", names("full-main-agent"), names("browser-sub-agent"), names("general-sub-agent"))
	}
	own, _ := Load(root, "browser-sub-agent")
	if _, ok := Find(own, "deploy"); ok {
		t.Error("the browser subagent has a skill of the main agent")
	}
	writeSkill(t, filepath.Join(root, "full-main-agent"), "review")
	if got := names("full-main-agent"); !slices.Equal(got, []string{"deploy", "review"}) {
		t.Errorf("after installing review: %q", got)
	}
	if err := Check(root, []string{"full-main-agent", "browser-sub-agent"}); err != nil {
		t.Errorf("a valid root: %v", err)
	}
}

func TestCheckRefusesASkillOutsideARoleAndADirectoryOfNoRole(t *testing.T) {
	flat := t.TempDir()
	writeSkill(t, flat, "deploy")
	if err := Check(flat, []string{"full-main-agent"}); err == nil || !strings.Contains(err.Error(), "outside a role's directory") {
		t.Errorf("a skill in the root: %v", err)
	}
	other := t.TempDir()
	writeSkill(t, filepath.Join(other, "main-agent"), "deploy")
	if err := Check(other, []string{"full-main-agent"}); err == nil || !strings.Contains(err.Error(), `"main-agent", which is no role`) {
		t.Errorf("skills of no role: %v", err)
	}
}

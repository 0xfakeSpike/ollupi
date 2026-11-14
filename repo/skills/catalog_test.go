package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

)

// A skill's resource stays inside its folder: no path, link or parent reaches a file beside it.
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
	if got, err := safeResourcePath(root, "docs/guide.md"); err != nil || got != filepath.Join(root, "docs", "guide.md") {
		t.Errorf("a file in the skill: %q %v", got, err)
	}
	for _, bad := range []string{"../secret.txt", "docs/../../secret.txt", filepath.Join(base, "secret.txt"), "docs/link.md", "..", "."} {
		if got, err := safeResourcePath(root, bad); err == nil {
			t.Errorf("%s reached %s", bad, got)
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

// Each role has the skills installed in the directory of its name, and no other role's.
func TestARoleHasTheSkillsOfItsDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "full-main-agent"), "deploy")
	writeSkill(t, filepath.Join(root, "compact-main-agent"), "deploy")
	writeSkill(t, filepath.Join(root, "browser-sub-agent"), "site-login")
	c, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Roles(); !slices.Equal(got, []string{"browser-sub-agent", "compact-main-agent", "full-main-agent"}) {
		t.Errorf("roles %q", got)
	}
	names := func(role string) []string {
		var out []string
		for _, s := range c.For(role) {
			out = append(out, s.Name)
		}
		return out
	}
	if !slices.Equal(names("full-main-agent"), []string{"deploy"}) || !slices.Equal(names("browser-sub-agent"), []string{"site-login"}) || names("general-sub-agent") != nil {
		t.Errorf("full %q, browser %q, general %q", names("full-main-agent"), names("browser-sub-agent"), names("general-sub-agent"))
	}
	if _, ok := c.Lookup("browser-sub-agent", "deploy"); ok {
		t.Error("the browser subagent has a skill of the main agent")
	}
	if c.Dir("browser-sub-agent") != filepath.Join(root, "browser-sub-agent") {
		t.Errorf("the browser subagent's directory: %s", c.Dir("browser-sub-agent"))
	}
}

func TestASkillOutsideARolesDirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy")
	if _, err := New(root); err == nil || !strings.Contains(err.Error(), "outside a role's directory") {
		t.Errorf("a skill in the root: %v", err)
	}
}

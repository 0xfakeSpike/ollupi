package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadDirTakesTheInstructionsAndTheTextFilesOfASkill(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "deploy")
	writeSkill(t, dir, map[string]string{
		"SKILL.md":           "---\nname: deploy\ndescription: How to deploy.\nmetadata:\n  version: 2\n---\nDo it step by step.\n",
		"docs/guide.md":      "guide",
		"scripts/run.sh":     "echo run",
		"image.png":          "not text",
		".hidden/secret.md":  "hidden",
		"../outside/note.md": "outside",
	})
	if err := os.Chmod(filepath.Join(dir, "scripts", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside", "note.md"), filepath.Join(dir, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	p, err := ReadDir(dir, []string{"full-main-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "deploy" || p.Description != "How to deploy." || p.Version != "2" || p.Instructions != "Do it step by step." {
		t.Errorf("the skill: %+v", p)
	}
	if len(p.Files) != 2 || p.Files[0].Path != "docs/guide.md" || p.Files[0].Executable || p.Files[1].Path != "scripts/run.sh" || !p.Files[1].Executable {
		t.Errorf("the files: %+v", p.Files)
	}
	if err := p.Check([]string{"full-main-agent"}); err != nil {
		t.Errorf("a valid skill: %v", err)
	}

	writeSkill(t, filepath.Join(base, "other"), map[string]string{"SKILL.md": "---\nname: deploy\ndescription: d\n---\nbody\n"})
	if _, err := ReadDir(filepath.Join(base, "other"), nil); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Errorf("a directory named apart from its skill: %v", err)
	}
}

func TestCheckRefusesASkillOfNoRoleAndAFileOutsideIt(t *testing.T) {
	roles := []string{"full-main-agent"}
	valid := func() Package {
		return Package{Name: "deploy", Description: "d", Instructions: "body", Roles: roles,
			Files: []File{{Path: "docs/guide.md", Body: []byte("guide")}}}
	}
	cases := map[string]func(*Package){
		"no role":      func(p *Package) { p.Roles = nil },
		"unknown role": func(p *Package) { p.Roles = []string{"main-agent"} },
		"bad name":     func(p *Package) { p.Name = "Deploy" },
		"parent":       func(p *Package) { p.Files[0].Path = "../secret.md" },
		"absolute":     func(p *Package) { p.Files[0].Path = "/etc/passwd.txt" },
		"unclean":      func(p *Package) { p.Files[0].Path = "docs/../guide.md" },
		"instructions": func(p *Package) { p.Files[0].Path = "SKILL.md" },
		"binary":       func(p *Package) { p.Files[0].Path = "image.png" },
		"not utf-8":    func(p *Package) { p.Files[0].Body = []byte{0xff} },
		"twice":        func(p *Package) { p.Files = append(p.Files, p.Files[0]) },
	}
	for name, change := range cases {
		p := valid()
		change(&p)
		if err := p.Check(roles); err == nil {
			t.Errorf("%s: checked", name)
		}
	}
}

func TestTheDigestCoversTheFiles(t *testing.T) {
	p := Package{Name: "deploy", Description: "d", Instructions: "body", Files: []File{{Path: "a.md", Body: []byte("a")}}}
	before := p.Skill().Digest
	p.Files[0].Body = []byte("b")
	if p.Skill().Digest == before {
		t.Error("a changed file left the digest")
	}
}

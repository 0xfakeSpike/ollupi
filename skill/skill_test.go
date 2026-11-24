package skill

import (
	"strings"
	"testing"
)

func TestCheckRefusesASkillOfNoRoleAndAFileOutsideIt(t *testing.T) {
	roles := []string{"full-main-agent", "code-sub-agent"}
	valid := func() Package {
		return Package{Name: "deploy", Description: "d", Instructions: "body", Roles: []string{"full-main-agent"},
			Files: []File{{Path: "docs/guide.md", Body: []byte("guide")}}}
	}
	if err := valid().Check(roles); err != nil {
		t.Fatalf("a valid skill: %v", err)
	}
	cases := map[string]func(*Package){
		"no role":      func(p *Package) { p.Roles = nil },
		"unknown role": func(p *Package) { p.Roles = []string{"main-agent"} },
		"role twice":   func(p *Package) { p.Roles = []string{"code-sub-agent", "code-sub-agent"} },
		"bad name":     func(p *Package) { p.Name = "Deploy" },
		"blank":        func(p *Package) { p.Instructions = " \n" },
		"parent":       func(p *Package) { p.Files[0].Path = "../secret.md" },
		"absolute":     func(p *Package) { p.Files[0].Path = "/etc/passwd.txt" },
		"unclean":      func(p *Package) { p.Files[0].Path = "docs/../guide.md" },
		"instructions": func(p *Package) { p.Files[0].Path = "SKILL.md" },
		"binary":       func(p *Package) { p.Files[0].Path = "image.png" },
		"not utf-8":    func(p *Package) { p.Files[0].Body = []byte{0xff} },
		"twice":        func(p *Package) { p.Files = append(p.Files, p.Files[0]) },
		"too large":    func(p *Package) { p.Files[0].Body = []byte(strings.Repeat("a", MaxBytes)) },
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

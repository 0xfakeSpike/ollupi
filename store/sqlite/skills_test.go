package sqlite

import (
	"errors"
	"testing"

	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestASkillIsReadByItsRolesAndReplacedWhole(t *testing.T) {
	s := open(t, t.TempDir())
	deploy := skill.Package{Name: "deploy", Description: "How to deploy.", Instructions: "Do it.", Roles: []string{"full-main-agent", "code-sub-agent"},
		Files: []skill.File{{Path: "scripts/run.sh", Body: []byte("echo run"), Executable: true}, {Path: "docs/guide.md", Body: []byte("guide")}}}
	login := skill.Package{Name: "site-login", Description: "How to log in.", Instructions: "Log in.", Roles: []string{"browser-sub-agent"}}
	update(t, s, func(tx store.Tx) error { return errors.Join(tx.PutSkill(deploy), tx.PutSkill(login)) })

	main := read(t, s, func(r store.Reader) ([]skill.Skill, error) { return r.Skills("full-main-agent") })
	if len(main) != 1 || main[0].Name != "deploy" || main[0].Digest != deploy.Skill().Digest || len(main[0].Resources) != 2 ||
		main[0].Resources[0] != (skill.Resource{Path: "docs/guide.md", ByteSize: 5}) {
		t.Fatalf("the main agent's skills: %+v", main)
	}
	got := read(t, s, func(r store.Reader) (skill.Skill, error) { return r.Skill("code-sub-agent", "deploy") })
	if got.Instructions != "Do it." {
		t.Errorf("the code subagent's deploy: %+v", got)
	}
	err := s.Read(t.Context(), func(r store.Reader) error { _, err := r.Skill("browser-sub-agent", "deploy"); return err })
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the browser subagent read deploy: %v", err)
	}
	files := read(t, s, func(r store.Reader) ([]skill.File, error) { return r.SkillFiles("deploy") })
	if len(files) != 2 || files[1].Path != "scripts/run.sh" || !files[1].Executable || string(files[1].Body) != "echo run" {
		t.Errorf("the files of deploy: %+v", files)
	}

	deploy.Roles = []string{"code-sub-agent"}
	deploy.Files = []skill.File{{Path: "docs/guide.md", Body: []byte("guide 2")}}
	update(t, s, func(tx store.Tx) error { return tx.PutSkill(deploy) })
	if main := read(t, s, func(r store.Reader) ([]skill.Skill, error) { return r.Skills("full-main-agent") }); len(main) != 0 {
		t.Errorf("a role taken off kept the skill: %+v", main)
	}
	err = s.Read(t.Context(), func(r store.Reader) error { _, err := r.SkillFile("deploy", "scripts/run.sh"); return err })
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a file taken off stayed: %v", err)
	}
	if body := read(t, s, func(r store.Reader) ([]byte, error) { return r.SkillFile("deploy", "docs/guide.md") }); string(body) != "guide 2" {
		t.Errorf("the replaced guide: %q", body)
	}

	update(t, s, func(tx store.Tx) error { return tx.DeleteSkill("deploy") })
	if files := read(t, s, func(r store.Reader) ([]skill.File, error) { return r.SkillFiles("deploy") }); len(files) != 0 {
		t.Errorf("the files of a deleted skill: %+v", files)
	}
	if err := s.Update(t.Context(), func(tx store.Tx) error { return tx.DeleteSkill("deploy") }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting a missing skill: %v", err)
	}
}

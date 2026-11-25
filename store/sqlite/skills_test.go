package sqlite

import (
	"errors"
	"github.com/0xfakeSpike/ollupi/account"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestAUsersSkillIsTheirsAndReplacedWhole(t *testing.T) {
	s := open(t, t.TempDir())
	update(t, s, func(tx store.Tx) error {
		return errors.Join(tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil)), tx.EnsureUser("v", account.NewEnvironment("This computer", "local", nil)))
	})
	deploy := skill.Package{Name: "deploy", Description: "How to deploy.", Instructions: "Do it.", Roles: []string{"full-main-agent", "code-sub-agent"},
		Files: []skill.File{{Path: "scripts/run.sh", Body: []byte("echo run"), Executable: true}, {Path: "docs/guide.md", Body: []byte("guide")}}}
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	update(t, s, func(tx store.Tx) error { return tx.PutSkill("u", deploy) })
	if len(heard) != 1 || heard[0].Owned == nil || heard[0].Owned.Skill == nil || heard[0].Owned.Skill.Digest != deploy.Skill().Digest {
		t.Fatalf("heard %+v", heard)
	}

	mine := read(t, s, func(r store.Reader) ([]skill.Skill, error) { return r.Skills("u") })
	if len(mine) != 1 || !slices.Equal(mine[0].Roles, []string{"code-sub-agent", "full-main-agent"}) ||
		len(mine[0].Resources) != 2 || mine[0].Resources[0] != (skill.Resource{Path: "docs/guide.md", ByteSize: 5}) {
		t.Fatalf("u's skills: %+v", mine)
	}
	if theirs := read(t, s, func(r store.Reader) ([]skill.Skill, error) { return r.Skills("v") }); len(theirs) != 0 {
		t.Errorf("v has u's skill: %+v", theirs)
	}
	if err := s.Read(t.Context(), func(r store.Reader) error { _, err := r.Skill("v", "deploy"); return err }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("v read u's deploy: %v", err)
	}
	got := read(t, s, func(r store.Reader) (skill.Skill, error) { return r.Skill("u", "deploy") })
	if got.Instructions != "Do it." {
		t.Errorf("u's deploy: %+v", got)
	}
	files := read(t, s, func(r store.Reader) ([]skill.File, error) { return r.SkillFiles("u", "deploy") })
	if len(files) != 2 || files[1].Path != "scripts/run.sh" || !files[1].Executable || string(files[1].Body) != "echo run" {
		t.Errorf("the files of deploy: %+v", files)
	}

	deploy.Roles = []string{"code-sub-agent"}
	deploy.Files = []skill.File{{Path: "docs/guide.md", Body: []byte("guide 2")}}
	update(t, s, func(tx store.Tx) error { return tx.PutSkill("u", deploy) })
	got = read(t, s, func(r store.Reader) (skill.Skill, error) { return r.Skill("u", "deploy") })
	if !slices.Equal(got.Roles, []string{"code-sub-agent"}) || len(got.Resources) != 1 {
		t.Errorf("the replaced deploy: %+v", got)
	}
	if body := read(t, s, func(r store.Reader) ([]byte, error) { return r.SkillFile("u", "deploy", "docs/guide.md") }); string(body) != "guide 2" {
		t.Errorf("the replaced guide: %q", body)
	}

	update(t, s, func(tx store.Tx) error { return tx.DeleteSkill("u", "deploy") })
	if last := heard[len(heard)-1]; last.Owned == nil || last.Owned.SkillDeleted != "deploy" {
		t.Errorf("heard the delete as %+v", last)
	}
	if files := read(t, s, func(r store.Reader) ([]skill.File, error) { return r.SkillFiles("u", "deploy") }); len(files) != 0 {
		t.Errorf("the files of a deleted skill: %+v", files)
	}
	if err := s.Update(t.Context(), func(tx store.Tx) error { return tx.DeleteSkill("u", "deploy") }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting a missing skill: %v", err)
	}
}

package app

import (
	"context"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) Skills(ctx context.Context, user string) (skills []skill.Skill, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if skills, err = r.Skills(user); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return skills, seq, err
}

func (a *App) Skill(ctx context.Context, user, name string) (s skill.Skill, files []skill.File, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if s, err = r.Skill(user, name); err != nil {
			return err
		}
		files, err = r.SkillFiles(user, name)
		return err
	})
	return s, files, err
}

func (a *App) SetSkill(ctx context.Context, user string, p skill.Package) (skill.Skill, error) {
	roles := make([]string, len(agent.Names))
	for i, n := range agent.Names {
		roles[i] = string(n)
	}
	if err := p.Check(roles); err != nil {
		return skill.Skill{}, err
	}
	return p.Skill(), a.st.Update(ctx, func(tx store.Tx) error { return tx.PutSkill(user, p) })
}

func (a *App) DeleteSkill(ctx context.Context, user, name string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.DeleteSkill(user, name) })
}

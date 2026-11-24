package sqlite

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
)

type skillRow struct {
	Name         string
	Description  string
	Version      string
	Instructions string
	Digest       string
}

type skillPartRow struct {
	Name     string
	Role     string
	Path     string
	ByteSize int64
}

func (r reader) Skills(owner string) ([]skill.Skill, error) {
	var rows []skillRow
	err := r.g.Raw("SELECT name, description, version, digest FROM skills WHERE user_id = ? ORDER BY name", owner).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: the skills of user %s: %w", owner, err)
	}
	var roles, files []skillPartRow
	if err := r.g.Raw("SELECT name, role FROM skill_roles WHERE user_id = ? ORDER BY name, role", owner).Scan(&roles).Error; err != nil {
		return nil, fmt.Errorf("store: the skill roles of user %s: %w", owner, err)
	}
	err = r.g.Raw("SELECT name, path, length(body) AS byte_size FROM skill_files WHERE user_id = ? ORDER BY name, path", owner).Scan(&files).Error
	if err != nil {
		return nil, fmt.Errorf("store: the skill files of user %s: %w", owner, err)
	}
	out := make([]skill.Skill, len(rows))
	for i, row := range rows {
		out[i] = row.skill(roles, files)
	}
	return out, nil
}

func (r reader) Skill(owner, name string) (skill.Skill, error) {
	var row skillRow
	if err := notFound(r.g.Raw("SELECT * FROM skills WHERE user_id = ? AND name = ?", owner, name).Take(&row).Error); err != nil {
		return skill.Skill{}, fmt.Errorf("store: skill %s: %w", name, err)
	}
	var roles, files []skillPartRow
	if err := r.g.Raw("SELECT name, role FROM skill_roles WHERE user_id = ? AND name = ? ORDER BY role", owner, name).Scan(&roles).Error; err != nil {
		return skill.Skill{}, fmt.Errorf("store: the roles of skill %s: %w", name, err)
	}
	err := r.g.Raw("SELECT name, path, length(body) AS byte_size FROM skill_files WHERE user_id = ? AND name = ? ORDER BY path", owner, name).Scan(&files).Error
	if err != nil {
		return skill.Skill{}, fmt.Errorf("store: the files of skill %s: %w", name, err)
	}
	return row.skill(roles, files), nil
}

func (row skillRow) skill(roles, files []skillPartRow) skill.Skill {
	s := skill.Skill{Name: row.Name, Description: row.Description, Version: row.Version,
		Instructions: row.Instructions, Digest: row.Digest, Roles: []string{}, Resources: []skill.Resource{}}
	for _, r := range roles {
		if r.Name == row.Name {
			s.Roles = append(s.Roles, r.Role)
		}
	}
	for _, f := range files {
		if f.Name == row.Name {
			s.Resources = append(s.Resources, skill.Resource{Path: f.Path, ByteSize: f.ByteSize})
		}
	}
	return s
}

func (r reader) SkillFile(owner, name, path string) ([]byte, error) {
	var row struct{ Body []byte }
	err := notFound(r.g.Raw("SELECT body FROM skill_files WHERE user_id = ? AND name = ? AND path = ?", owner, name, path).Take(&row).Error)
	if err != nil {
		return nil, fmt.Errorf("store: file %s of skill %s: %w", path, name, err)
	}
	return row.Body, nil
}

func (r reader) SkillFiles(owner, name string) ([]skill.File, error) {
	var files []skill.File
	err := r.g.Raw("SELECT path, body, executable FROM skill_files WHERE user_id = ? AND name = ? ORDER BY path", owner, name).Scan(&files).Error
	if err != nil {
		return nil, fmt.Errorf("store: the files of skill %s: %w", name, err)
	}
	return files, nil
}

func (t *tx) PutSkill(owner string, p skill.Package) error {
	s := p.Skill()
	err := t.g.Exec(`INSERT INTO skills(user_id, name, description, version, instructions, digest) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, name) DO UPDATE SET description = excluded.description, version = excluded.version,
			instructions = excluded.instructions, digest = excluded.digest`,
		owner, s.Name, s.Description, s.Version, s.Instructions, s.Digest).Error
	if err != nil {
		return fmt.Errorf("store: skill %s: %w", s.Name, err)
	}
	if err := t.g.Exec("DELETE FROM skill_roles WHERE user_id = ? AND name = ?", owner, s.Name).Error; err != nil {
		return fmt.Errorf("store: the roles of skill %s: %w", s.Name, err)
	}
	for _, role := range s.Roles {
		if err := t.g.Exec("INSERT INTO skill_roles(user_id, name, role) VALUES (?, ?, ?)", owner, s.Name, role).Error; err != nil {
			return fmt.Errorf("store: role %s of skill %s: %w", role, s.Name, err)
		}
	}
	if err := t.g.Exec("DELETE FROM skill_files WHERE user_id = ? AND name = ?", owner, s.Name).Error; err != nil {
		return fmt.Errorf("store: the files of skill %s: %w", s.Name, err)
	}
	for _, f := range p.Files {
		err := t.g.Exec("INSERT INTO skill_files(user_id, name, path, body, executable) VALUES (?, ?, ?, ?, ?)",
			owner, s.Name, f.Path, f.Body, f.Executable).Error
		if err != nil {
			return fmt.Errorf("store: file %s of skill %s: %w", f.Path, s.Name, err)
		}
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{Skill: &s}})
	return nil
}

func (t *tx) DeleteSkill(owner, name string) error {
	if err := one(t.g.Exec("DELETE FROM skills WHERE user_id = ? AND name = ?", owner, name), "skill "+name); err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{User: owner, Owned: &store.Owned{SkillDeleted: name}})
	return nil
}

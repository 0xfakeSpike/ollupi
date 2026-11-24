package sqlite

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/skill"
)

type skillRow struct {
	Name         string
	Description  string
	Version      string
	Instructions string
	Digest       string
}

type resourceRow struct {
	Name     string
	Path     string
	ByteSize int64
}

func (r reader) Skills(role string) ([]skill.Skill, error) {
	var rows []skillRow
	err := r.g.Raw(`SELECT s.name, s.description, s.version, s.digest FROM skills s
		JOIN skill_roles r ON r.name = s.name WHERE r.role = ? ORDER BY s.name`, role).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: the skills of %s: %w", role, err)
	}
	var resources []resourceRow
	err = r.g.Raw(`SELECT f.name, f.path, length(f.body) AS byte_size FROM skill_files f
		JOIN skill_roles r ON r.name = f.name WHERE r.role = ? ORDER BY f.name, f.path`, role).Scan(&resources).Error
	if err != nil {
		return nil, fmt.Errorf("store: the skill files of %s: %w", role, err)
	}
	byName := map[string][]skill.Resource{}
	for _, f := range resources {
		byName[f.Name] = append(byName[f.Name], skill.Resource{Path: f.Path, ByteSize: f.ByteSize})
	}
	out := make([]skill.Skill, len(rows))
	for i, row := range rows {
		out[i] = row.skill(byName[row.Name])
	}
	return out, nil
}

func (r reader) Skill(role, name string) (skill.Skill, error) {
	var row skillRow
	err := notFound(r.g.Raw(`SELECT s.* FROM skills s JOIN skill_roles r ON r.name = s.name
		WHERE r.role = ? AND s.name = ?`, role, name).Take(&row).Error)
	if err != nil {
		return skill.Skill{}, fmt.Errorf("store: skill %s of %s: %w", name, role, err)
	}
	var resources []skill.Resource
	err = r.g.Raw("SELECT path, length(body) AS byte_size FROM skill_files WHERE name = ? ORDER BY path", name).Scan(&resources).Error
	if err != nil {
		return skill.Skill{}, fmt.Errorf("store: the files of skill %s: %w", name, err)
	}
	return row.skill(resources), nil
}

func (row skillRow) skill(resources []skill.Resource) skill.Skill {
	return skill.Skill{Name: row.Name, Description: row.Description, Version: row.Version,
		Instructions: row.Instructions, Digest: row.Digest, Resources: nonNil(resources)}
}

func (r reader) SkillFile(name, path string) ([]byte, error) {
	var row struct{ Body []byte }
	if err := notFound(r.g.Raw("SELECT body FROM skill_files WHERE name = ? AND path = ?", name, path).Take(&row).Error); err != nil {
		return nil, fmt.Errorf("store: file %s of skill %s: %w", path, name, err)
	}
	return row.Body, nil
}

func (r reader) SkillFiles(name string) ([]skill.File, error) {
	var files []skill.File
	if err := r.g.Raw("SELECT path, body, executable FROM skill_files WHERE name = ? ORDER BY path", name).Scan(&files).Error; err != nil {
		return nil, fmt.Errorf("store: the files of skill %s: %w", name, err)
	}
	return files, nil
}

func (t *tx) PutSkill(p skill.Package) error {
	s := p.Skill()
	err := t.g.Exec(`INSERT INTO skills(name, description, version, instructions, digest) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description, version = excluded.version,
			instructions = excluded.instructions, digest = excluded.digest`,
		s.Name, s.Description, s.Version, s.Instructions, s.Digest).Error
	if err != nil {
		return fmt.Errorf("store: skill %s: %w", s.Name, err)
	}
	if err := t.g.Exec("DELETE FROM skill_roles WHERE name = ?", s.Name).Error; err != nil {
		return fmt.Errorf("store: the roles of skill %s: %w", s.Name, err)
	}
	for _, role := range p.Roles {
		if err := t.g.Exec("INSERT INTO skill_roles(name, role) VALUES (?, ?)", s.Name, role).Error; err != nil {
			return fmt.Errorf("store: role %s of skill %s: %w", role, s.Name, err)
		}
	}
	if err := t.g.Exec("DELETE FROM skill_files WHERE name = ?", s.Name).Error; err != nil {
		return fmt.Errorf("store: the files of skill %s: %w", s.Name, err)
	}
	for _, f := range p.Files {
		err := t.g.Exec("INSERT INTO skill_files(name, path, body, executable) VALUES (?, ?, ?, ?)", s.Name, f.Path, f.Body, f.Executable).Error
		if err != nil {
			return fmt.Errorf("store: file %s of skill %s: %w", f.Path, s.Name, err)
		}
	}
	return nil
}

func (t *tx) DeleteSkill(name string) error {
	return one(t.g.Exec("DELETE FROM skills WHERE name = ?", name), "skill "+name)
}

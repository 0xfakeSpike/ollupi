package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxDescriptionRunes   = 1024
	maxInstructionRunes   = 20000
	maxInstructionLines   = 500
	defaultResourceRunes  = 12000
	MaxResourceRunes      = 30000
	maxReadableResourceB  = 1 << 20
	maxListedResources    = 200
	skillInstructionsFile = "SKILL.md"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Skill struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	SourceFile   string     `json:"source_file"`
	Version      string     `json:"version"`
	Resources    []Resource `json:"resources"`
	ByteSize     int64      `json:"byte_size"`
	RootDir      string     `json:"-"`
	Instructions string     `json:"-"`
	Digest       string     `json:"-"`
}

type Resource struct {
	Path     string `json:"path"`
	ByteSize int64  `json:"byte_size"`
}

type ResourcePage struct {
	Skill           string `json:"skill"`
	Path            string `json:"path"`
	Text            string `json:"text"`
	OffsetRunes     int    `json:"offset_runes"`
	LimitRunes      int    `json:"limit_runes"`
	TotalRunes      int    `json:"total_runes"`
	NextOffsetRunes int    `json:"next_offset_runes"`
	EOF             bool   `json:"eof"`
}

func Load(root, role string) ([]Skill, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	dir := filepath.Join(root, role)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	return scanRole(root, dir)
}

func Check(root string, roles []string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("skills: read root %q: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Lstat(filepath.Join(dir, skillInstructionsFile)); err == nil {
			return fmt.Errorf("skills: %q is a skill outside a role's directory; move it to %s", dir, filepath.Join(root, "<role>", entry.Name()))
		}
		if !slices.Contains(roles, entry.Name()) {
			return fmt.Errorf("skills: %s has skills for %q, which is no role; the roles are %q", root, entry.Name(), roles)
		}
		if _, err := scanRole(root, dir); err != nil {
			return err
		}
	}
	return nil
}

func Find(skills []Skill, name string) (Skill, bool) {
	i := slices.IndexFunc(skills, func(s Skill) bool { return s.Name == strings.TrimSpace(name) })
	if i < 0 {
		return Skill{}, false
	}
	return skills[i], true
}

func (skill Skill) Page(resourcePath string, offset, limit int) (ResourcePage, error) {
	resourcePath = filepath.ToSlash(strings.TrimSpace(resourcePath))
	if resourcePath == "" {
		return ResourcePage{}, fmt.Errorf("skills: resource path is required")
	}
	exposed := func(r Resource) bool { return r.Path == resourcePath }
	if !slices.ContainsFunc(skill.Resources, exposed) {
		return ResourcePage{}, fmt.Errorf("skills: resource %q is not exposed by skill %q", resourcePath, skill.Name)
	}
	if limit <= 0 {
		limit = defaultResourceRunes
	}

	raw, err := readResource(skill.RootDir, resourcePath)
	if err != nil {
		return ResourcePage{}, err
	}
	if !utf8.Valid(raw) {
		return ResourcePage{}, fmt.Errorf("skills: resource %q is not valid UTF-8", resourcePath)
	}
	runes := []rune(string(raw))
	if offset > len(runes) {
		offset = len(runes)
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return ResourcePage{
		Skill:           skill.Name,
		Path:            resourcePath,
		Text:            string(runes[offset:end]),
		OffsetRunes:     offset,
		LimitRunes:      limit,
		TotalRunes:      len(runes),
		NextOffsetRunes: end,
		EOF:             end >= len(runes),
	}, nil
}

type frontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
}

func scanRole(root, dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: read %q: %w", dir, err)
	}
	var loaded []Skill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		sourcePath := filepath.Join(skillDir, skillInstructionsFile)
		info, err := os.Lstat(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("skills: stat %q: %w", sourcePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("skills: %q must be a regular non-symlink file", sourcePath)
		}
		skill, err := parseSkill(root, skillDir, sourcePath, info.Size())
		if err != nil {
			return nil, err
		}
		if skill.Name != entry.Name() {
			return nil, fmt.Errorf("skills: directory %q must match frontmatter name %q", entry.Name(), skill.Name)
		}
		loaded = append(loaded, skill)
	}
	return loaded, nil
}

func parseSkill(root, skillDir, sourcePath string, byteSize int64) (Skill, error) {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: read %q: %w", sourcePath, err)
	}
	if !utf8.Valid(raw) {
		return Skill{}, fmt.Errorf("skills: %q is not valid UTF-8", sourcePath)
	}
	fm, instructions, err := parseDocument(string(raw))
	if err != nil {
		return Skill{}, fmt.Errorf("skills: parse %q: %w", sourcePath, err)
	}
	name := strings.TrimSpace(fm.Name)
	if !skillNamePattern.MatchString(name) || len([]rune(name)) > 64 {
		return Skill{}, fmt.Errorf("invalid name %q; use 1-64 lowercase letters, numbers, and hyphens", name)
	}
	description := strings.TrimSpace(fm.Description)
	if description == "" {
		return Skill{}, fmt.Errorf("description is required")
	}
	if len([]rune(description)) > maxDescriptionRunes {
		return Skill{}, fmt.Errorf("description exceeds %d runes", maxDescriptionRunes)
	}
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return Skill{}, fmt.Errorf("instruction body is required")
	}
	if len([]rune(instructions)) > maxInstructionRunes {
		return Skill{}, fmt.Errorf("instruction body exceeds %d runes; move details into references/", maxInstructionRunes)
	}
	if lines := strings.Count(instructions, "\n") + 1; lines > maxInstructionLines {
		return Skill{}, fmt.Errorf("instruction body has %d lines; maximum is %d", lines, maxInstructionLines)
	}
	resources, err := scanResources(skillDir)
	if err != nil {
		return Skill{}, err
	}
	sourceFile, _ := filepath.Rel(root, sourcePath)
	digestBytes := sha256.Sum256(raw)
	return Skill{
		Name:         name,
		Description:  description,
		Version:      version(fm.Metadata),
		RootDir:      skillDir,
		SourceFile:   filepath.ToSlash(sourceFile),
		Instructions: instructions,
		Digest:       hex.EncodeToString(digestBytes[:]),
		ByteSize:     byteSize,
		Resources:    resources,
	}, nil
}

func parseDocument(text string) (frontmatter, string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return frontmatter{}, "", fmt.Errorf("YAML frontmatter is required")
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return frontmatter{}, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &fm); err != nil {
		return frontmatter{}, "", err
	}
	return fm, strings.Join(lines[closing+1:], "\n"), nil
}

func scanResources(skillDir string) ([]Resource, error) {
	resources := make([]Resource, 0)
	err := filepath.WalkDir(skillDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == skillDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == skillInstructionsFile || !isReadableResource(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(skillDir, path)
		resources = append(resources, Resource{Path: filepath.ToSlash(rel), ByteSize: info.Size()})
		if len(resources) > maxListedResources {
			return fmt.Errorf("skill %q exposes more than %d readable resources", skillDir, maxListedResources)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skills: scan resources in %q: %w", skillDir, err)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Path < resources[j].Path })
	return resources, nil
}

func isReadableResource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".go", ".py", ".js", ".ts", ".sh":
		return true
	default:
		return false
	}
}

func readResource(root, resourcePath string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("skills: open the skill: %w", err)
	}
	defer r.Close()
	name := filepath.FromSlash(resourcePath)
	info, err := r.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("skills: resource %q: %w", resourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skills: resource %q must be a regular non-symlink file", resourcePath)
	}
	if info.Size() > maxReadableResourceB {
		return nil, fmt.Errorf("skills: resource %q exceeds %d bytes", resourcePath, maxReadableResourceB)
	}
	raw, err := r.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("skills: read resource %q: %w", resourcePath, err)
	}
	return raw, nil
}

func version(metadata map[string]any) string {
	v, ok := metadata["version"]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

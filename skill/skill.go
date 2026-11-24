package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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
	maxFileBytes          = 1 << 20
	maxFiles              = 200
	skillInstructionsFile = "SKILL.md"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Skill struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Version      string     `json:"version"`
	Resources    []Resource `json:"resources"`
	Instructions string     `json:"-"`
	Digest       string     `json:"-"`
}

type Resource struct {
	Path     string `json:"path"`
	ByteSize int64  `json:"byte_size"`
}

type File struct {
	Path       string
	Body       []byte
	Executable bool
}

type Package struct {
	Name         string
	Description  string
	Version      string
	Instructions string
	Roles        []string
	Files        []File
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

func ReadDir(dir string, roles []string) (Package, error) {
	raw, err := os.ReadFile(filepath.Join(dir, skillInstructionsFile))
	if err != nil {
		return Package{}, fmt.Errorf("skills: %w", err)
	}
	fm, instructions, err := parseDocument(string(raw))
	if err != nil {
		return Package{}, fmt.Errorf("skills: parse %s in %q: %w", skillInstructionsFile, dir, err)
	}
	p := Package{
		Name:         strings.TrimSpace(fm.Name),
		Description:  strings.TrimSpace(fm.Description),
		Version:      version(fm.Metadata),
		Instructions: strings.TrimSpace(instructions),
		Roles:        roles,
	}
	if p.Name != filepath.Base(dir) {
		return Package{}, fmt.Errorf("skills: directory %q must match frontmatter name %q", filepath.Base(dir), p.Name)
	}
	if p.Files, err = readFiles(dir); err != nil {
		return Package{}, err
	}
	return p, nil
}

func (p Package) Check(roles []string) error {
	if !skillNamePattern.MatchString(p.Name) || len(p.Name) > 64 {
		return fmt.Errorf("skills: invalid name %q; use 1-64 lowercase letters, numbers, and hyphens", p.Name)
	}
	if p.Description == "" {
		return fmt.Errorf("skills: %s: description is required", p.Name)
	}
	if utf8.RuneCountInString(p.Description) > maxDescriptionRunes {
		return fmt.Errorf("skills: %s: description exceeds %d runes", p.Name, maxDescriptionRunes)
	}
	if p.Instructions == "" {
		return fmt.Errorf("skills: %s: instruction body is required", p.Name)
	}
	if utf8.RuneCountInString(p.Instructions) > maxInstructionRunes {
		return fmt.Errorf("skills: %s: instruction body exceeds %d runes; move details into references/", p.Name, maxInstructionRunes)
	}
	if lines := strings.Count(p.Instructions, "\n") + 1; lines > maxInstructionLines {
		return fmt.Errorf("skills: %s: instruction body has %d lines; maximum is %d", p.Name, lines, maxInstructionLines)
	}
	if len(p.Roles) == 0 {
		return fmt.Errorf("skills: %s: at least one role is required", p.Name)
	}
	for _, role := range p.Roles {
		if !slices.Contains(roles, role) {
			return fmt.Errorf("skills: %s: %q is no role; the roles are %q", p.Name, role, roles)
		}
	}
	if len(p.Files) > maxFiles {
		return fmt.Errorf("skills: %s has more than %d files", p.Name, maxFiles)
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if err := checkFile(f); err != nil {
			return fmt.Errorf("skills: %s: %w", p.Name, err)
		}
		if seen[f.Path] {
			return fmt.Errorf("skills: %s: file %q is listed twice", p.Name, f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

func checkFile(f File) error {
	local := filepath.IsLocal(filepath.FromSlash(f.Path)) && path.Clean(f.Path) == f.Path && !strings.Contains(f.Path, `\`)
	if !local {
		return fmt.Errorf("file %q must be a relative path inside the skill", f.Path)
	}
	if f.Path == skillInstructionsFile {
		return fmt.Errorf("%s is the instructions, not a file", skillInstructionsFile)
	}
	if !isReadableResource(f.Path) {
		return fmt.Errorf("file %q is not a readable text file", f.Path)
	}
	if len(f.Body) > maxFileBytes {
		return fmt.Errorf("file %q exceeds %d bytes", f.Path, maxFileBytes)
	}
	if !utf8.Valid(f.Body) {
		return fmt.Errorf("file %q is not valid UTF-8", f.Path)
	}
	return nil
}

func (p Package) Skill() Skill {
	files := slices.SortedFunc(slices.Values(p.Files), func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	resources := make([]Resource, len(files))
	for i, f := range files {
		resources[i] = Resource{Path: f.Path, ByteSize: int64(len(f.Body))}
	}
	canonical, _ := json.Marshal(struct {
		Name, Description, Version, Instructions string
		Files                                    []File
	}{p.Name, p.Description, p.Version, p.Instructions, files})
	sum := sha256.Sum256(canonical)
	return Skill{Name: p.Name, Description: p.Description, Version: p.Version, Resources: resources,
		Instructions: p.Instructions, Digest: hex.EncodeToString(sum[:])}
}

func Page(skill, resourcePath string, body []byte, offset, limit int) ResourcePage {
	if limit <= 0 {
		limit = defaultResourceRunes
	}
	runes := []rune(string(body))
	offset = min(offset, len(runes))
	end := min(offset+limit, len(runes))
	return ResourcePage{
		Skill:           skill,
		Path:            resourcePath,
		Text:            string(runes[offset:end]),
		OffsetRunes:     offset,
		LimitRunes:      limit,
		TotalRunes:      len(runes),
		NextOffsetRunes: end,
		EOF:             end >= len(runes),
	}
}

type frontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
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

func readFiles(dir string) ([]File, error) {
	var files []File
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
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
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, File{Path: filepath.ToSlash(rel), Body: body, Executable: info.Mode()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skills: read the files in %q: %w", dir, err)
	}
	return files, nil
}

func isReadableResource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".go", ".py", ".js", ".ts", ".sh":
		return true
	default:
		return false
	}
}

func version(metadata map[string]any) string {
	v, ok := metadata["version"]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxDescriptionRunes   = 1024
	maxInstructionRunes   = 20000
	maxInstructionLines   = 500
	defaultResourceRunes  = 12000
	MaxResourceRunes      = 30000
	MaxBytes              = 2 << 20
	maxFiles              = 200
	skillInstructionsFile = "SKILL.md"
)

var ErrInvalid = errors.New("invalid skill")

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Skill struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Version      string     `json:"version"`
	Resources    []Resource `json:"resources"`
	Roles        []string   `json:"-"`
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

func (p Package) Check(roles []string) error {
	if err := p.check(roles); err != nil {
		return fmt.Errorf("%w %q: %w", ErrInvalid, p.Name, err)
	}
	return nil
}

func (p Package) check(roles []string) error {
	if !skillNamePattern.MatchString(p.Name) || len(p.Name) > 64 {
		return errors.New("use 1-64 lowercase letters, numbers, and hyphens for the name")
	}
	if strings.TrimSpace(p.Description) == "" {
		return errors.New("description is required")
	}
	if utf8.RuneCountInString(p.Description) > maxDescriptionRunes {
		return fmt.Errorf("description exceeds %d runes", maxDescriptionRunes)
	}
	if strings.TrimSpace(p.Instructions) == "" {
		return errors.New("instructions are required")
	}
	if utf8.RuneCountInString(p.Instructions) > maxInstructionRunes {
		return fmt.Errorf("instructions exceed %d runes; move details into files", maxInstructionRunes)
	}
	if lines := strings.Count(p.Instructions, "\n") + 1; lines > maxInstructionLines {
		return fmt.Errorf("instructions have %d lines; maximum is %d", lines, maxInstructionLines)
	}
	if len(p.Roles) == 0 {
		return errors.New("at least one role is required")
	}
	for i, role := range p.Roles {
		if !slices.Contains(roles, role) {
			return fmt.Errorf("%q is no role; the roles are %q", role, roles)
		}
		if slices.Contains(p.Roles[:i], role) {
			return fmt.Errorf("role %q is listed twice", role)
		}
	}
	if len(p.Files) > maxFiles {
		return fmt.Errorf("more than %d files", maxFiles)
	}
	size := len(p.Instructions)
	seen := map[string]bool{}
	for _, f := range p.Files {
		if err := checkFile(f); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("file %q is listed twice", f.Path)
		}
		seen[f.Path] = true
		size += len(f.Body)
	}
	if size > MaxBytes {
		return fmt.Errorf("the instructions and files exceed %d bytes", MaxBytes)
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
		Roles: slices.Sorted(slices.Values(p.Roles)), Instructions: p.Instructions, Digest: hex.EncodeToString(sum[:])}
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

func isReadableResource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".go", ".py", ".js", ".ts", ".sh":
		return true
	default:
		return false
	}
}

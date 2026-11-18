package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/prompt"
	skills "github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	providerName      = "skills"
	toolGetRootDir    = "get_skills_root_dir"
	toolListSkills    = "list_skills"
	toolActivateSkill = "activate_skill"
	toolReadResource  = "read_skill_resource"
)

type Tools struct {
	root string
}

func New(root string) *Tools { return &Tools{root: root} }

func (*Tools) Name() (string, string) {
	return providerName, "Directory-based Agent Skills with explicit activation and on-demand resources."
}

func (t *Tools) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	switch c.Tool {
	case toolGetRootDir:
		return kit.Result{Output: map[string]any{"skills_root_dir": filepath.Join(t.root, c.Role)}}, nil
	case toolListSkills:
		own, err := skills.Load(t.root, c.Role)
		if err != nil {
			return kit.Result{}, err
		}
		return kit.Result{Output: map[string]any{"skills": own}}, nil
	case toolActivateSkill:
		return t.activate(c.Role, c.Arguments)
	}
	return t.readResource(c.Role, c.Arguments)
}

type activateArgs struct {
	Name string `json:"name" jsonschema:"Exact skill name from AVAILABLE SKILLS."`
}

type resourceArgs struct {
	Skill       string `json:"skill"`
	Path        string `json:"path" jsonschema:"Resource path relative to the skill directory, as activate_skill listed it."`
	OffsetRunes int    `json:"offset_runes,omitempty"`
	LimitRunes  int    `json:"limit_runes,omitempty"`
}

func (resourceArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["offset_runes"].Minimum = jsonschema.Ptr(0.0)
	s.Properties["limit_runes"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["limit_runes"].Maximum = jsonschema.Ptr(float64(skills.MaxResourceRunes))
}

func (*Tools) Tools(context.Context) ([]kit.Spec, error) {
	read := tool.Annotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	return []kit.Spec{
		{
			Name:        toolGetRootDir,
			Annotations: read,
			Description: "Return the directory of your Agent Skills, where each of them lives.",
			InputSchema: kit.InputSchema[struct{}](),
		},
		{
			Name:        toolListSkills,
			Annotations: read,
			Description: "List available Agent Skills and their supporting resource files.",
			InputSchema: kit.InputSchema[struct{}](),
		},
		{
			Name:        toolActivateSkill,
			Annotations: read,
			Description: "Activate one exact Agent Skill. Its complete SKILL.md instructions remain active for the conversation.",
			InputSchema: kit.InputSchema[activateArgs](),
		},
		{
			Name:        toolReadResource,
			Annotations: read,
			Description: "Read a supporting UTF-8 resource from an activated Agent Skill. Use only paths listed for that skill.",
			InputSchema: kit.InputSchema[resourceArgs](),
		},
	}, nil
}

func (t *Tools) activate(role string, arguments map[string]any) (kit.Result, error) {
	args, _ := kit.DecodeArguments[activateArgs](arguments)
	skill, err := t.find(role, args.Name)
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{
		Output: map[string]any{
			"activated":   skill.Name,
			"description": skill.Description,
			"version":     skill.Version,
			"digest":      skill.Digest,
			"resources":   skill.Resources,
		},
		Artifacts: []conversation.Artifact{{
			Kind:    prompt.SkillArtifact,
			Name:    skill.Name,
			Content: skill.Instructions,
		}},
	}, nil
}

func (t *Tools) readResource(role string, arguments map[string]any) (kit.Result, error) {
	args, err := kit.DecodeArguments[resourceArgs](arguments)
	if err != nil {
		return kit.Result{}, err
	}
	skill, err := t.find(role, args.Skill)
	if err != nil {
		return kit.Result{}, err
	}
	page, err := skill.Page(args.Path, args.OffsetRunes, args.LimitRunes)
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: page}, nil
}

func (t *Tools) find(role, name string) (skills.Skill, error) {
	own, err := skills.Load(t.root, role)
	if err != nil {
		return skills.Skill{}, err
	}
	skill, ok := skills.Find(own, name)
	if !ok {
		return skills.Skill{}, fmt.Errorf("skills: you have no skill named %q; use a name from AVAILABLE SKILLS", strings.TrimSpace(name))
	}
	return skill, nil
}

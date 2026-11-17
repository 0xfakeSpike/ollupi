// Package skills gives an agent the tools of the skills its role has: listing them, activating one, and reading its
// resources.
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
	ProviderName      = "skills"
	ToolGetRootDir    = "get_skills_root_dir"
	ToolListSkills    = "list_skills"
	ToolActivateSkill = "activate_skill"
	ToolReadResource  = "read_skill_resource"
)

type Tools struct {
	root string
}

func New(root string) *Tools { return &Tools{root: root} }

func (*Tools) Name() (string, string) {
	return ProviderName, "Directory-based Agent Skills with explicit activation and on-demand resources."
}

func (t *Tools) Invoke(_ context.Context, c tool.Call) (kit.Result, error) {
	caller, toolName, arguments := c.Caller, c.Tool, c.Arguments
	switch strings.TrimSpace(toolName) {
	case ToolGetRootDir:
		return kit.Result{Output: map[string]any{"skills_root_dir": filepath.Join(t.root, caller.Role)}}, nil
	case ToolListSkills:
		own, err := skills.Load(t.root, caller.Role)
		if err != nil {
			return kit.Result{}, err
		}
		return kit.Result{Output: map[string]any{"skills": own}}, nil
	case ToolActivateSkill:
		return t.activate(caller.Role, arguments)
	case ToolReadResource:
		return t.readResource(caller.Role, arguments)
	default:
		err := fmt.Errorf("skills: unknown tool %q", toolName)
		return kit.Result{}, err
	}
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
	read := tool.Annotations{ReadOnlyHint: true, OpenWorldHint: kit.Hint(false)}
	return []kit.Spec{
		{
			Name:        ToolGetRootDir,
			Annotations: read,
			Description: "Return the directory of your Agent Skills, where each of them lives.",
			InputSchema: kit.InputSchema[struct{}](),
		},
		{
			Name:        ToolListSkills,
			Annotations: read,
			Description: "List available Agent Skills and their supporting resource files.",
			InputSchema: kit.InputSchema[struct{}](),
		},
		{
			Name:        ToolActivateSkill,
			Annotations: read,
			Description: "Activate one exact Agent Skill. Its complete SKILL.md instructions remain active for the conversation.",
			InputSchema: kit.InputSchema[activateArgs](),
		},
		{
			Name:        ToolReadResource,
			Annotations: read,
			Description: "Read a supporting UTF-8 resource from an activated Agent Skill. Use only paths listed for that skill.",
			InputSchema: kit.InputSchema[resourceArgs](),
		},
	}, nil
}

func (t *Tools) activate(role string, arguments map[string]any) (kit.Result, error) {
	args, _ := kit.DecodeArguments[activateArgs](arguments) // the schema checked its fields: it cannot fail
	name := strings.TrimSpace(args.Name)
	skill, ok, err := t.find(role, name)
	if err != nil {
		return kit.Result{}, err
	}
	if !ok {
		return kit.Result{}, fmt.Errorf("skills: you have no skill named %q; use a name from AVAILABLE SKILLS", name)
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
	skill, ok, err := t.find(role, args.Skill)
	if err != nil {
		return kit.Result{}, err
	}
	if !ok {
		return kit.Result{}, fmt.Errorf("skills: you have no skill named %q; use a name from AVAILABLE SKILLS", args.Skill)
	}
	page, err := skill.Page(args.Path, args.OffsetRunes, args.LimitRunes)
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: page}, nil
}

func (t *Tools) find(role, name string) (skills.Skill, bool, error) {
	own, err := skills.Load(t.root, role)
	if err != nil {
		return skills.Skill{}, false, err
	}
	skill, ok := skills.Find(own, name)
	return skill, ok, nil
}

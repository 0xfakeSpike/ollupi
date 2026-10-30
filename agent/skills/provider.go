package skills

import (
	"context"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"strings"

	"github.com/0xfakeSpike/ollupi/tools"
)

const (
	ProviderName      = "skills"
	ToolGetRootDir    = "get_skills_root_dir"
	ToolListSkills    = "list_skills"
	ToolActivateSkill = "activate_skill"
	ToolReadResource  = "read_skill_resource"
)

func (c *Catalog) Name() (string, string) {
	return ProviderName, "Directory-based Agent Skills with explicit activation and on-demand resources."
}

func (c *Catalog) Invoke(_ context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	switch strings.TrimSpace(tool) {
	case ToolGetRootDir:
		return tools.Result{Output: map[string]any{"skills_root_dir": c.root}}, nil
	case ToolListSkills:
		return tools.Result{Output: map[string]any{"skills": c.All()}}, nil
	case ToolActivateSkill:
		return c.activate(arguments)
	case ToolReadResource:
		return c.readResource(arguments)
	default:
		err := fmt.Errorf("skills: unknown tool %q", tool)
		return tools.Result{}, err
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
	s.Properties["limit_runes"].Maximum = jsonschema.Ptr(float64(maxResourceRunes))
}

func (c *Catalog) ToolList(context.Context) ([]*tools.Spec, error) {
	var names []any
	for _, name := range c.names() {
		names = append(names, name)
	}
	activate, resource := tools.InputSchema[activateArgs](), tools.InputSchema[resourceArgs]()
	activate.Properties["name"].Enum, resource.Properties["skill"].Enum = names, names

	read := &tools.Annotations{ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)}
	return []*tools.Spec{
		{
			Name:        ToolGetRootDir,
			Annotations: read,
			Description: "Return the Agent Skills root directory, where every skill lives.",
			InputSchema: tools.InputSchema[struct{}](),
		},
		{
			Name:        ToolListSkills,
			Annotations: read,
			Description: "List available Agent Skills and their supporting resource files.",
			InputSchema: tools.InputSchema[struct{}](),
		},
		{
			Name:        ToolActivateSkill,
			Annotations: read,
			Description: "Activate one exact Agent Skill. Its complete SKILL.md instructions remain active for the conversation.",
			InputSchema: activate,
		},
		{
			Name:        ToolReadResource,
			Annotations: read,
			Description: "Read a supporting UTF-8 resource from an activated Agent Skill. Use only paths listed for that skill.",
			InputSchema: resource,
		},
	}, nil
}

func (c *Catalog) activate(arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[activateArgs](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	name := strings.TrimSpace(args.Name)
	skill, ok := c.lookup(name)
	if !ok {
		return tools.Result{}, fmt.Errorf("skills: no skill named %q", name)
	}
	return tools.Result{
		Output: map[string]any{
			"activated":   skill.Name,
			"description": skill.Description,
			"version":     skill.Version,
			"digest":      skill.Digest,
			"resources":   skill.Resources,
		},
		Artifacts: []tools.ContextArtifact{{
			Kind:    tools.ArtifactAgentSkill,
			Name:    skill.Name,
			Content: skill.Instructions,
		}},
	}, nil
}

func (c *Catalog) readResource(arguments map[string]any) (tools.Result, error) {
	args, err := tools.DecodeArguments[resourceArgs](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	page, err := c.resourcePage(args.Skill, args.Path, args.OffsetRunes, args.LimitRunes)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Output: page}, nil
}

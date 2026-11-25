package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	providerName      = "skills"
	toolListSkills    = "list_skills"
	toolActivateSkill = "activate_skill"
	toolReadResource  = "read_skill_resource"
	digestFile        = ".digest"
)

type Provider struct {
	st    store.Store
	boxes sandbox.Boxes
}

// New puts a skill's files in the environment of the conversation that
// loads it, where its commands run.
func New(st store.Store, boxes sandbox.Boxes) *Provider {
	return &Provider{st: st, boxes: boxes}
}

func (*Provider) Name() (string, string) {
	return providerName, "Agent Skills with explicit activation and on-demand resources."
}

func (p *Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	switch c.Tool {
	case toolListSkills:
		var all []skill.Skill
		err := p.st.Read(ctx, func(rd store.Reader) (err error) {
			all, err = rd.Skills(c.User)
			return err
		})
		if err != nil {
			return kit.Result{}, err
		}
		role, _ := agent.RoleOf(agent.Name(c.Role))
		own := make([]skill.Skill, 0, len(all))
		for _, s := range all {
			if role.HasSkill(s.Roles) {
				own = append(own, s)
			}
		}
		return kit.Result{Output: map[string]any{"skills": own}}, nil
	case toolActivateSkill:
		return p.activate(ctx, c)
	}
	return p.readResource(ctx, c.Caller, c.Arguments)
}

func ownSkill(rd store.Reader, c tool.Caller, name string) (skill.Skill, error) {
	s, err := rd.Skill(c.User, name)
	role, _ := agent.RoleOf(agent.Name(c.Role))
	missing := errors.Is(err, store.ErrNotFound) || err == nil && !role.HasSkill(s.Roles)
	if missing {
		return skill.Skill{}, fmt.Errorf("skills: you have no skill named %q; use a name from AVAILABLE SKILLS", name)
	}
	return s, err
}

type activateArgs struct {
	Name string `json:"name" jsonschema:"Exact skill name from AVAILABLE SKILLS."`
}

type resourceArgs struct {
	Skill       string `json:"skill"`
	Path        string `json:"path" jsonschema:"Resource path relative to the skill, as activate_skill listed it."`
	OffsetRunes int    `json:"offset_runes,omitempty"`
	LimitRunes  int    `json:"limit_runes,omitempty"`
}

func (resourceArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["offset_runes"].Minimum = jsonschema.Ptr(0.0)
	s.Properties["limit_runes"].Minimum = jsonschema.Ptr(1.0)
	s.Properties["limit_runes"].Maximum = jsonschema.Ptr(float64(skill.MaxResourceRunes))
}

func (*Provider) Tools(context.Context) ([]kit.Spec, error) {
	read := tool.Annotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	return []kit.Spec{
		{
			Name:        toolListSkills,
			Annotations: read,
			Description: "List available Agent Skills and their supporting resource files.",
			InputSchema: kit.InputSchema[struct{}](),
		},
		{
			Name:        toolActivateSkill,
			Annotations: read,
			Description: "Activate one exact Agent Skill. Its complete SKILL.md instructions remain active for the conversation, and its resource files are put in the sandbox, in the directory it returns.",
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

func (p *Provider) activate(ctx context.Context, c tool.Call) (kit.Result, error) {
	args, _ := kit.DecodeArguments[activateArgs](c.Arguments)
	name := strings.TrimSpace(args.Name)
	var found skill.Skill
	var files []skill.File
	err := p.st.Read(ctx, func(rd store.Reader) error {
		var err error
		if found, err = ownSkill(rd, c.Caller, name); err != nil {
			return err
		}
		files, err = rd.SkillFiles(c.User, name)
		return err
	})
	if err != nil {
		return kit.Result{}, err
	}
	out := map[string]any{
		"activated":   found.Name,
		"description": found.Description,
		"version":     found.Version,
		"digest":      found.Digest,
		"resources":   found.Resources,
	}
	if len(files) > 0 {
		dir, err := p.place(ctx, c.Caller, found, files)
		if err != nil {
			return kit.Result{}, err
		}
		out["directory"] = dir
	}
	return kit.Result{
		Output: out,
		Artifacts: []conversation.Artifact{{
			Kind:    prompt.SkillArtifact,
			Name:    found.Name,
			Content: found.Instructions,
		}},
	}, nil
}

func (p *Provider) place(ctx context.Context, c tool.Caller, s skill.Skill, files []skill.File) (string, error) {
	box, err := p.boxes(ctx, c.User, c.Environment)
	if err != nil {
		return "", fmt.Errorf("skills: the environment of the conversation: %w", err)
	}
	roots := box.Roots()
	if len(roots) == 0 {
		return "", fmt.Errorf("skills: the sandbox has no folder to put the files of %s in", s.Name)
	}
	dir := filepath.Join(roots[0], ".ollupi", "skills", s.Name)
	marker := filepath.Join(dir, digestFile)
	if have, err := sandbox.ReadFile(ctx, box, marker, int64(len(s.Digest))); err == nil && string(have) == s.Digest {
		return dir, nil
	}
	for _, f := range files {
		perm := fs.FileMode(0o644)
		if f.Executable {
			perm = 0o755
		}
		if err := box.WriteFile(ctx, filepath.Join(dir, filepath.FromSlash(f.Path)), f.Body, perm); err != nil {
			return "", fmt.Errorf("skills: put %s of %s in the sandbox: %w", f.Path, s.Name, err)
		}
	}
	if err := box.WriteFile(ctx, marker, []byte(s.Digest), 0o644); err != nil {
		return "", fmt.Errorf("skills: put %s in the sandbox: %w", s.Name, err)
	}
	return dir, nil
}

func (p *Provider) readResource(ctx context.Context, c tool.Caller, arguments map[string]any) (kit.Result, error) {
	args, err := kit.DecodeArguments[resourceArgs](arguments)
	if err != nil {
		return kit.Result{}, err
	}
	name := strings.TrimSpace(args.Skill)
	resourcePath := strings.TrimSpace(args.Path)
	var body []byte
	err = p.st.Read(ctx, func(rd store.Reader) error {
		if _, err := ownSkill(rd, c, name); err != nil {
			return err
		}
		var err error
		if body, err = rd.SkillFile(c.User, name, resourcePath); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("skills: resource %q is not exposed by skill %q", resourcePath, name)
		}
		return err
	})
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: skill.Page(name, resourcePath, body, args.OffsetRunes, args.LimitRunes)}, nil
}

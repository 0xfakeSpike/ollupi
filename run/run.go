package run

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
)

type Models interface {
	Limits(user string, m llm.Model) (llm.Limits, error)
	Client(user string, m llm.Model) (llm.Client, error)
}

type Attachments interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

type Skills func(role agent.Name) ([]prompt.Skill, error)

type Runner struct {
	st          store.Store
	models      Models
	tools       tool.Tools
	attachments Attachments
	skills      Skills
	name        string
	prompt      string
	deployment  policy.Settings
	optIn       []string
	allowlist   []string
}

type Deployment struct {
	Name        string
	Prompt      string
	Permissions policy.Settings
	Allowlist   []string
	OptIn       []string
}

func New(st store.Store, models Models, tools tool.Tools, attachments Attachments, skills Skills, d Deployment) *Runner {
	return &Runner{st: st, models: models, tools: tools, attachments: attachments, skills: skills,
		name: d.Name, prompt: d.Prompt, deployment: d.Permissions, allowlist: d.Allowlist, optIn: d.OptIn}
}

func (r *Runner) Run(ctx context.Context, thread string) error {
	if err := r.closeStranded(ctx, thread); err != nil {
		return err
	}
	var set tool.Set
	for {
		s, err := r.load(ctx, thread)
		if err != nil || s == nil {
			return err
		}
		if set == nil {
			if set, err = r.tools(ctx, s.th.User); err != nil {
				return fmt.Errorf("run: the tools of the user: %w", err)
			}
		}
		if err := s.permit(r, set); err != nil {
			return err
		}
		switch a := agent.Next(s.v, s.role).(type) {
		case agent.Wait:
			return nil
		case agent.Finish:
			return r.finish(ctx, s)
		case agent.RunTools:
			err = r.runTools(ctx, s, a.Calls)
		case agent.SkipTools:
			err = r.skip(ctx, s, a)
		case agent.CallModel:
			err = r.callModel(ctx, s, a)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
	}
}

const stranded = "the call was cut off when the run that made it stopped; it may have taken effect in part or in full, so check before trying it again"

func (r *Runner) closeStranded(ctx context.Context, thread string) error {
	return r.st.Update(ctx, func(tx store.Tx) error {
		v, err := tx.View(thread)
		if err != nil {
			return err
		}
		turn, ok := v.Active()
		if !ok {
			return nil
		}
		var entries []conversation.Entry
		for _, it := range v.ItemsOf(turn.ID) {
			cutOff := it.Kind == conversation.ItemToolCall && it.Status == conversation.InProgress && !it.WaitsForInput()
			if cutOff {
				entries = append(entries, conversation.Entry{Turn: turn.ID, Item: it.ID, Body: conversation.ToolResult{Error: stranded}})
			}
		}
		if len(entries) == 0 {
			return nil
		}
		_, err = tx.Append(thread, entries...)
		return err
	})
}

type step struct {
	th        store.Thread
	workspace *account.Workspace
	settings  account.Settings
	v         conversation.View
	items     []conversation.Item
	turn      conversation.Turn
	role      agent.Role
	window    prompt.Window
	redactor  *redact.Redactor

	set      tool.Set
	checker  *policy.Checker
	specs    map[string]tool.Spec
	sent     []prompt.Tool
	deferred []prompt.Tool
}

func (r *Runner) load(ctx context.Context, thread string) (*step, error) {
	s := &step{}
	var active bool
	err := r.st.Read(ctx, func(rd store.Reader) (err error) {
		if s.th, err = rd.Thread(thread); err != nil {
			return err
		}
		if s.v, err = rd.View(thread); err != nil {
			return err
		}
		if s.turn, active = s.v.Active(); !active {
			return nil
		}
		s.items = prompt.Transcript(s.v)
		if s.settings, err = rd.Settings(s.th.User); err != nil {
			return err
		}
		if s.th.Workspace != "" {
			w, err := rd.Workspace(s.th.User, s.th.Workspace)
			if err != nil {
				return err
			}
			s.workspace = &w
		}
		return nil
	})
	if err != nil || !active {
		return nil, err
	}
	role, ok := agent.RoleOf(agent.Name(s.turn.Role))
	if !ok {
		return nil, fmt.Errorf("run: turn %s names role %q, which is none of %q", s.turn.ID, s.turn.Role, agent.Names)
	}
	limits, err := r.models.Limits(s.th.User, s.turn.Model)
	if err != nil {
		return nil, fmt.Errorf("run: model %s: %w", s.turn.Model.Name, err)
	}
	s.role, s.window, s.redactor = role, prompt.NewWindow(s.turn.Model.Name, limits), redact.New(s.settings.RedactionKey)
	return s, nil
}

func (s *step) permit(r *Runner, set tool.Set) error {
	layers := slices.Concat(s.role.Own, account.Layers(account.Deployment(r.deployment), r.optIn,
		s.settings, s.workspace, s.th.Layer()))
	box := tool.Toolbox(set)
	s.specs = map[string]tool.Spec{}
	var allowlist []string
	if r.allowlist != nil {
		allowlist = []string{}
	}
	for _, t := range set.Specs() {
		s.specs[t.Name] = t
		if slices.Contains(r.allowlist, t.Name) {
			allowlist = append(allowlist, t.Name)
		}
	}
	checker, err := policy.New(layers, allowlist, box)
	if err != nil {
		return fmt.Errorf("run: the permission settings: %w", err)
	}
	var visible []prompt.Tool
	for _, t := range agent.Launchable(checker.Tools()) {
		spec := s.specs[t.Name]
		visible = append(visible, prompt.Tool{Name: spec.Name, Provider: spec.Provider, Description: spec.Description, Schema: spec.Schema})
	}
	s.set, s.checker = set, checker
	s.sent, s.deferred = s.window.Offer(visible, s.role.Core, s.items)
	return nil
}

func (s *step) caller(item string) tool.Caller {
	c := tool.Caller{User: s.th.User, Thread: s.th.ID, Workspace: s.th.Workspace, Environment: s.th.Environment,
		Subagent: s.th.Parent != "", Role: string(s.role.Name), Item: item}
	if s.workspace != nil {
		c.Root = s.workspace.Root
	}
	for _, t := range s.deferred {
		c.Deferred = append(c.Deferred, t.Name)
	}
	return c
}

func (r *Runner) append(ctx context.Context, thread string, entries ...conversation.Entry) error {
	return r.st.Update(ctx, func(tx store.Tx) error {
		_, err := tx.Append(thread, entries...)
		return err
	})
}

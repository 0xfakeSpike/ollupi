// Package run runs a turn: it reads the conversation, asks agent.Next what to do, does it, and appends what came of it
// to the log, a step at a time, until the turn waits or ends. Every step reads the log afresh, so a message the user
// sends meanwhile, or a decision, is part of the next one; and a turn picked up after a crash goes on as if it had
// never stopped.
package run

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
)

// Models are the models turns name: their limits, and the client a user's turns call one through.
type Models interface {
	Limits(m llm.Model) (llm.Limits, error)
	Client(user string, m llm.Model) (llm.Client, error)
}

// Attachments keeps the files users attach to their messages.
type Attachments interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// Skills are the skills a role is told of.
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
	allowlist   []string
}

// New takes the deployment's name for the agent and its prompt for main agents, "" for the default, its permission
// settings, beneath every user's, and allowlist, unless nil every tool it lets be offered.
func New(st store.Store, models Models, tools tool.Tools, attachments Attachments, skills Skills,
	name, prompt string, deployment policy.Settings, allowlist []string) *Runner {
	return &Runner{st: st, models: models, tools: tools, attachments: attachments, skills: skills,
		name: name, prompt: prompt, deployment: deployment, allowlist: allowlist}
}

// Run runs the turn in progress of thread until it waits, ends, or ctx ends. An error is the turn's failure, which the
// caller ends it with.
//
// Whoever calls Run runs the thread alone, so a call it finds started and not ended was cut off with the run before,
// and ends as interrupted.
func (r *Runner) Run(ctx context.Context, thread string) error {
	if err := r.settle(ctx, thread); err != nil {
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

// stranded is how a call ends that a run before left started: it may have done some of its work, or all of it.
const stranded = "the call was cut off when the run that made it stopped; it may have taken effect in part or in full, so check before trying it again"

func (r *Runner) settle(ctx context.Context, thread string) error {
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
			waitsForAnswer := it.Input != nil && !it.Input.Answered
			if it.Kind == conversation.ItemToolCall && it.Status == conversation.InProgress && !waitsForAnswer {
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

// step is what one step of a turn reads.
type step struct {
	th        store.Thread
	parent    *store.Thread
	workspace *user.Workspace
	settings  user.Settings
	v         conversation.View
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

// load reads thread on one snapshot; nil when it has no turn in progress.
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
		if s.th.Parent != "" {
			p, err := rd.Thread(s.th.Parent)
			if err != nil {
				return err
			}
			s.parent = &p
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
	limits, err := r.models.Limits(s.turn.Model)
	if err != nil {
		return nil, fmt.Errorf("run: model %s: %w", s.turn.Model.Name, err)
	}
	s.role, s.window, s.redactor = role, prompt.NewWindow(s.turn.Model.Name, limits), redact.New(s.settings.RedactionKey)
	return s, nil
}

// permit decides which of set's tools the turn may see, by the layers as they are now: the settings may have changed
// since the step before.
func (s *step) permit(r *Runner, set tool.Set) error {
	conversationLayer := s.th.Layer()
	if s.parent != nil {
		conversationLayer = s.parent.ParentLayer()
	}
	layers := slices.Concat(s.role.Own, user.Layers(policy.Layer{Of: "the default permission settings", Settings: r.deployment, Shared: true},
		s.settings, s.workspace, conversationLayer))
	box := policy.Toolbox{Unavailable: set.Unavailable()}
	s.specs = map[string]tool.Spec{}
	var allowlist []string
	if r.allowlist != nil {
		allowlist = []string{}
	}
	for _, t := range set.Specs() {
		s.specs[t.Name] = t
		box.Tools = append(box.Tools, policy.Tool{Name: t.Name, Provider: t.Provider, Content: t.Content})
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
	items, _ := prompt.Transcript(s.v)
	s.set, s.checker = set, checker
	s.sent, s.deferred = s.window.Offer(visible, s.role.Core, items)
	return nil
}

func (s *step) caller(item string) tool.Caller {
	c := tool.Caller{User: s.th.User, Thread: s.th.ID, Workspace: s.th.Workspace, Environment: s.th.Environment,
		Subagent: s.parent != nil, Role: string(s.role.Name), Item: item}
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

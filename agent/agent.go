package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

const (
	maxInputRunes = 4000
)

var (
	ErrNoActiveTurn      = errors.New("the thread has no turn in progress")
	ErrTurnMismatch      = errors.New("the expected turn is not the turn in progress")
	ErrNoSuchRequest     = errors.New("the thread does not wait on this request")
	ErrSubagentThread    = errors.New("a subagent thread takes no input; send it to its parent conversation")
	ErrWorkspaceArchived = errors.New("the workspace is archived; restore it before starting new work")
	ErrInvalidMessage    = errors.New("invalid message")
	ErrInvalidAnswer     = errors.New("invalid answer")
	ErrInvalidDecision   = errors.New("invalid decision")
	ErrInvalidName       = errors.New("invalid name")
)

type Config struct {
	Name     string
	Prompt   string
	Models   []llm.Model
	Tools    []string
	Settings permission.Settings
	Slots    int

	Connections map[string]*llm.OpenAI
	Providers   []tools.Provider
	SkillsDir   string
	Memory      *memory.Manager
	DB          *repo.DB
	OnEvent     func(Event)
}

func New(cfg Config) *Agent {
	return &Agent{cfg: cfg}
}

func (a *Agent) Start(ctx context.Context) error {
	if strings.TrimSpace(a.cfg.Name) == "" {
		return fmt.Errorf("agent: name is required")
	}
	if a.cfg.DB == nil {
		return fmt.Errorf("agent: a database is required")
	}
	a.ctx = ctx
	if err := a.buildTools(ctx); err != nil {
		return err
	}
	if err := a.checkModels(); err != nil {
		return err
	}
	if err := a.checkBudgets(); err != nil {
		return fmt.Errorf("agent %s: %w", a.cfg.Name, err)
	}
	if a.cfg.Slots <= 0 {
		a.cfg.Slots = defaultSlots
	}
	a.events, a.slotByThread = make(chan func()), map[string]*slot{}
	if err := a.recover(); err != nil {
		return err
	}
	a.start()
	return nil
}

func (a *Agent) buildTools(ctx context.Context) error {
	providers := append(slices.Clone(a.cfg.Providers), plan.New(), subagent.New(), userinput.New(), contextmanager.NewOutputs(a.cfg.DB))
	if a.cfg.SkillsDir != "" {
		var err error
		if a.skills, err = skills.New(a.cfg.SkillsDir); err != nil {
			return fmt.Errorf("agent: skills: %w", err)
		}
		providers = append(providers, a.skills)
	}
	var err error
	if a.registry, err = tools.New(ctx, providers...); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

func (a *Agent) CallTool(ctx context.Context, threadID, tool string, arguments map[string]any) (tools.Result, error) {
	caller := tools.Caller{}
	if threadID != "" {
		t, err := a.cfg.DB.ThreadRow(threadID)
		if err != nil {
			return tools.Result{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
		}
		w, err := a.workspaceOf(t.WorkspaceID)
		if err != nil {
			return tools.Result{}, err
		}
		caller = tools.Caller{ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), Root: w.RootPath, Subagent: t.ParentThreadID != ""}
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return a.registry.Call(ctx, caller, tool, arguments), nil
}

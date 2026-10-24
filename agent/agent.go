package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
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

type Agent struct {
	Name     string
	Prompt   string
	Models   []llm.Model
	Tools    []string
	Settings permission.Settings

	Connections map[string]*llm.OpenAI
	Providers   []tools.Provider
	SkillsDir   string
	Memory      *memory.Manager
	DB          *repo.DB
	OnEvent     func(Event)

	ctx      context.Context
	registry *tools.Registry
	skills   *skills.Catalog
	run      *orchestrator
}

func (a *Agent) Start(ctx context.Context) error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("agent: name is required")
	}
	if a.DB == nil {
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
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}
	a.run = newOrchestrator(a, ctx)
	if err := a.run.recover(); err != nil {
		return err
	}
	a.run.start()
	return nil
}

func (a *Agent) buildTools(ctx context.Context) error {
	providers := append(slices.Clone(a.Providers), plan.New(), subagent.New(), userinput.New())
	if a.SkillsDir != "" {
		var err error
		if a.skills, err = skills.New(a.SkillsDir); err != nil {
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

func (a *Agent) Wait() {
	a.run.wait()
}

func (a *Agent) CallTool(ctx context.Context, threadID, tool string, arguments map[string]any) (tools.Result, error) {
	caller := tools.Caller{}
	if threadID != "" {
		t, err := a.DB.ThreadRow(threadID)
		if err != nil {
			return tools.Result{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
		}
		caller = a.callerOf(t)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return a.registry.Call(ctx, caller, tool, arguments), nil
}

func (a *Agent) callerOf(t repo.ThreadRow) tools.Caller {
	return tools.Caller{ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), Root: a.environmentOf(t).workspace.RootPath, Subagent: t.ParentThreadID != ""}
}

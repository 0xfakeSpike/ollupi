package agent

import (
	"context"
	"errors"
	"fmt"
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

func (a *orchestrator) boot(ctx context.Context) error {
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
	a.events, a.work, a.slots = make(chan func()), make(chan work), map[string]*slot{}
	if err := a.recover(); err != nil {
		return err
	}
	a.start()
	return nil
}

func (a *orchestrator) buildTools(ctx context.Context) error {
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

func (a *orchestrator) callTool(ctx context.Context, threadID, tool string, arguments map[string]any) (tools.Result, error) {
	caller := tools.Caller{}
	if threadID != "" {
		t, err := a.DB.ThreadRow(threadID)
		if err != nil {
			return tools.Result{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
		}
		env, err := a.environmentOf(t)
		if err != nil {
			return tools.Result{}, err
		}
		caller = env.caller(t)
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return a.registry.Call(ctx, caller, tool, arguments), nil
}

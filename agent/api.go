package agent

import (
	"context"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

type Config struct {
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
}

type Agent struct {
	o *orchestrator
}

func New(cfg Config) *Agent {
	return &Agent{o: &orchestrator{Config: cfg}}
}

func (a *Agent) Start(ctx context.Context) error { return a.o.boot(ctx) }

func (a *Agent) Wait() { a.o.wait() }

func (a *Agent) Memory() *memory.Manager { return a.o.Memory }

func (a *Agent) ListModels() ModelList { return a.o.listModels() }

func (a *Agent) CallTool(ctx context.Context, threadID, tool string, arguments map[string]any) (tools.Result, error) {
	return a.o.callTool(ctx, threadID, tool, arguments)
}

func (a *Agent) StartThread(workspaceID string, settings permission.Settings) (repo.ThreadRow, error) {
	return a.o.startThread(workspaceID, settings)
}

func (a *Agent) UpdateThreadSettings(threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	return a.o.updateThreadSettings(threadID, settings)
}

func (a *Agent) ReadThread(id string) (repo.ThreadView, error) { return a.o.readThread(id) }

func (a *Agent) ReadThreadPage(id string, before, limit int) (repo.ThreadView, bool, error) {
	return a.o.readThreadPage(id, before, limit)
}

func (a *Agent) ListThreads(f repo.ThreadFilter) ([]repo.ThreadSummary, error) {
	return a.o.listThreads(f)
}

func (a *Agent) PendingRequests(threadID string) []Request { return a.o.pendingRequests(threadID) }

func (a *Agent) AllPendingRequests() []Request { return a.o.allPendingRequests() }

func (a *Agent) ListWorkspaces() ([]repo.WorkspaceRow, error) { return a.o.listWorkspaces() }

func (a *Agent) AddWorkspace(path, name string) (repo.WorkspaceRow, error) {
	return a.o.addWorkspace(path, name)
}

func (a *Agent) RenameWorkspace(id, name string) (repo.WorkspaceRow, error) {
	return a.o.renameWorkspace(id, name)
}

func (a *Agent) ArchiveWorkspace(id string, archived bool) (repo.WorkspaceRow, error) {
	return a.o.archiveWorkspace(id, archived)
}

func (a *Agent) UpdateWorkspaceSettings(id string, settings permission.Settings) (repo.WorkspaceRow, error) {
	return a.o.updateWorkspaceSettings(id, settings)
}

func (a *Agent) StartTurn(threadID string, in Input) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.o.call(func() (err error) {
		turn, err = a.o.startTurn(threadID, in)
		return err
	})
	return turn, err
}

func (a *Agent) SteerTurn(threadID, expectedTurnID, text string) (repo.TurnRow, error) {
	var turn repo.TurnRow
	err := a.o.call(func() (err error) {
		turn, err = a.o.steer(threadID, expectedTurnID, text)
		return err
	})
	return turn, err
}

func (a *Agent) InterruptTurn(threadID, expectedTurnID string) error {
	return a.o.call(func() error { return a.o.interrupt(threadID, expectedTurnID) })
}

func (a *Agent) ResolveApproval(threadID, approvalID string, decision repo.Decision, decidedBy string) error {
	return a.o.call(func() error { return a.o.resolveApproval(threadID, approvalID, decision, decidedBy) })
}

func (a *Agent) AnswerUserInput(threadID, itemID string, answers map[string]userinput.Answer) error {
	return a.o.call(func() error { return a.o.answerUserInput(threadID, itemID, answers) })
}

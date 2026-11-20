package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) modelList(ctx context.Context, _ Empty) (ModelListResult, error) {
	models, fallback := c.s.app.Models()
	return ModelListResult{Data: models, Default: fallback}, nil
}

func (c *conn) toolList(ctx context.Context, _ Empty) (ToolListResult, error) {
	specs, err := c.s.app.Tools(ctx, c.user)
	out := make([]ToolDescriptor, len(specs))
	for i, s := range specs {
		out[i] = ToolDescriptor{Provider: s.Provider, Name: s.Name, Description: s.Description, Annotations: s.Annotations, InputSchema: s.Schema}
	}
	return ToolListResult{Data: out}, err
}

func (c *conn) mcpServerList(ctx context.Context, _ Empty) (MCPServerListResult, error) {
	servers, err := c.s.app.MCPServers(ctx, c.user)
	out := make([]MCPServer, len(servers))
	for i, s := range servers {
		out[i] = MCPServer{MCPServerConfig: mcpServerConfig(s.MCPServer)}
		if st := s.Status; st != nil {
			out[i].Status = &MCPServerStatus{Name: st.Name, Connected: st.Connected, Tools: st.Tools, SkippedTools: st.Skipped, Error: st.Error}
		}
	}
	return MCPServerListResult{Data: out}, err
}

func (c *conn) mcpServerSet(ctx context.Context, p MCPServerConfig) (MCPServerConfig, error) {
	s := account.MCPServer{Name: p.Name, Command: p.Command, Args: p.Args, Env: p.Env, URL: p.URL, Headers: p.Headers}
	return p, c.s.app.SetMCPServer(ctx, c.user, s)
}

func (c *conn) mcpServerDelete(ctx context.Context, p MCPServerDeleteParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteMCPServer(ctx, c.user, p.Name)
}

func (c *conn) workspaceList(ctx context.Context, _ Empty) (WorkspaceListResult, error) {
	ws, err := c.s.app.Workspaces(ctx, c.user)
	return WorkspaceListResult{Data: each(ws, workspaceRow)}, err
}

func (c *conn) workspaceAdd(ctx context.Context, p WorkspaceAddParams) (WorkspaceResult, error) {
	w, err := c.s.app.AddWorkspace(ctx, c.user, p.Path, p.Name)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspaceRename(ctx context.Context, p WorkspaceRenameParams) (WorkspaceResult, error) {
	w, err := c.s.app.RenameWorkspace(ctx, c.user, p.WorkspaceID, p.Name)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspaceMove(ctx context.Context, p WorkspaceMoveParams) (WorkspaceResult, error) {
	w, err := c.s.app.MoveWorkspace(ctx, c.user, p.WorkspaceID, p.Path)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspaceArchive(ctx context.Context, p WorkspaceArchiveParams) (WorkspaceResult, error) {
	if p.Archived == nil {
		return WorkspaceResult{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	w, err := c.s.app.ArchiveWorkspace(ctx, c.user, p.WorkspaceID, *p.Archived)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspaceInstructions(ctx context.Context, p WorkspaceInstructionsParams) (WorkspaceResult, error) {
	w, err := c.s.app.UpdateWorkspaceInstructions(ctx, c.user, p.WorkspaceID, p.Instructions)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspaceProviders(ctx context.Context, p WorkspaceProvidersParams) (WorkspaceResult, error) {
	w, err := c.s.app.UpdateWorkspaceProviders(ctx, c.user, p.WorkspaceID, p.Providers)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspacePermissions(ctx context.Context, p WorkspacePermissionsParams) (WorkspaceResult, error) {
	w, err := c.s.app.UpdateWorkspacePermissions(ctx, c.user, p.WorkspaceID, p.Settings)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) environmentList(ctx context.Context, _ Empty) (EnvironmentListResult, error) {
	envs, err := c.s.app.Environments(ctx, c.user)
	return EnvironmentListResult{Data: each(envs, environmentRow)}, err
}

func (c *conn) environmentAdd(ctx context.Context, p EnvironmentAddParams) (EnvironmentResult, error) {
	e, err := c.s.app.AddEnvironment(ctx, c.user, p.Name, p.Kind)
	return EnvironmentResult{Environment: environmentRow(e)}, err
}

func (c *conn) environmentRename(ctx context.Context, p EnvironmentRenameParams) (EnvironmentResult, error) {
	e, err := c.s.app.RenameEnvironment(ctx, c.user, p.EnvironmentID, p.Name)
	return EnvironmentResult{Environment: environmentRow(e)}, err
}

func (c *conn) environmentDelete(ctx context.Context, p EnvironmentDeleteParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteEnvironment(ctx, c.user, p.EnvironmentID)
}

func (c *conn) cronList(ctx context.Context, p CronListParams) (CronListResult, error) {
	jobs, err := c.s.app.CronJobs(ctx, c.user, p.ThreadID)
	return CronListResult{Data: each(jobs, cronJobRow)}, err
}

func (c *conn) cronCreate(ctx context.Context, p CronCreateParams) (CronResult, error) {
	j, err := c.s.app.CreateCronJob(ctx, c.user, p.ThreadID, cron.Draft{Name: p.Name, Spec: p.Spec, Prompt: p.Prompt, Times: p.Times})
	return CronResult{Job: cronJobRow(j)}, err
}

func (c *conn) cronPause(ctx context.Context, p CronPauseParams) (CronResult, error) {
	if p.Paused == nil {
		return CronResult{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
	}
	j, err := c.s.app.PauseCronJob(ctx, c.user, p.ThreadID, p.Name, *p.Paused)
	return CronResult{Job: cronJobRow(j)}, err
}

func (c *conn) cronDelete(ctx context.Context, p CronDeleteParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteCronJob(ctx, c.user, p.ThreadID, p.Name)
}

func (c *conn) memoryList(ctx context.Context, p MemoryListParams) (MemoryListResult, error) {
	ms, err := c.s.app.Memories(ctx, c.user, memory.Place{Scope: p.Scope, Workspace: p.WorkspaceID})
	return MemoryListResult{Data: each(ms, memoryRow)}, err
}

func (c *conn) memoryStatus(ctx context.Context, _ Empty) (MemoryStatus, error) {
	s, err := c.s.app.MemoryStatus(ctx, c.user)
	if err != nil {
		return MemoryStatus{}, err
	}
	settings, err := c.s.app.Settings(ctx, c.user)
	return MemoryStatus{Enabled: settings.Memory, Unread: s.Unread, Active: s.Active, Attempts: s.Attempts, NextAttempt: s.NextAttempt,
		Skipped: s.Skipped, LastSuccess: s.LastSuccess, LastFailure: s.LastFailure, LastSkip: s.LastSkip}, err
}

func (c *conn) memoryUpdate(ctx context.Context, p MemoryUpdateParams) (MemoryResult, error) {
	m, err := c.s.app.EditMemory(ctx, c.user, p.ID, p.Text)
	return MemoryResult{Memory: memoryRow(m)}, err
}

func (c *conn) memoryMove(ctx context.Context, p MemoryMoveParams) (MemoryResult, error) {
	m, err := c.s.app.MoveMemory(ctx, c.user, p.ID)
	return MemoryResult{Memory: memoryRow(m)}, err
}

func (c *conn) memoryForget(ctx context.Context, p MemoryForgetParams) (Empty, error) {
	return Empty{}, c.s.app.ForgetMemory(ctx, c.user, p.ID)
}

func (c *conn) search(ctx context.Context, p SearchParams) (SearchResult, error) {
	if err := limit(&p.Limit, defaultLimit, maxLimit); err != nil {
		return SearchResult{}, err
	}
	f, notes, err := c.s.app.Search(ctx, c.user, p.Query, p.WorkspaceID, p.Limit)
	return SearchResult{Found: found(f), Memories: each(notes, memoryRow)}, err
}

func (c *conn) settingsRead(ctx context.Context, _ Empty) (SettingsRow, error) {
	s, err := c.s.app.Settings(ctx, c.user)
	return settingsRow(s), err
}

func (c *conn) settingsUpdate(ctx context.Context, p SettingsUpdateParams) (SettingsRow, error) {
	if p.Redaction == nil && p.Memory == nil && p.Learn == nil {
		return SettingsRow{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "give redaction, memory or learn")
	}
	s, err := c.s.app.UpdateSettings(ctx, c.user, p.Redaction, p.Memory, p.Learn)
	return settingsRow(s), err
}

func (c *conn) settingsPermissions(ctx context.Context, p SettingsPermissionsParams) (SettingsRow, error) {
	s, err := c.s.app.UpdateUserPermissions(ctx, c.user, p.Settings)
	return settingsRow(s), err
}

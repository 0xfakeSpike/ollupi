package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/tool/mcp"
	"github.com/0xfakeSpike/ollupi/user"
)

func (c *conn) modelList(ctx context.Context, _ Empty) (ModelListResult, func(), error) {
	models, fallback := c.s.app.Models()
	return ModelListResult{Data: models, Default: fallback}, nil, nil
}

func (c *conn) toolList(ctx context.Context, _ Empty) (ToolListResult, func(), error) {
	specs, err := c.s.app.Tools(ctx, c.user)
	out := make([]ToolDescriptor, len(specs))
	for i, s := range specs {
		out[i] = ToolDescriptor{Provider: s.Provider, Name: s.Name, Description: s.Description, Annotations: s.Annotations, InputSchema: s.Schema}
	}
	return ToolListResult{Data: out}, nil, err
}

func (c *conn) mcpServerList(ctx context.Context, _ Empty) (MCPServerListResult, func(), error) {
	servers, err := c.s.app.MCPServers(ctx, c.user)
	if err != nil {
		return MCPServerListResult{}, nil, err
	}
	statuses, err := c.s.mcp.Statuses(c.user)
	if err != nil {
		return MCPServerListResult{}, nil, err
	}
	byName := map[string]mcp.ServerStatus{}
	for _, st := range statuses {
		byName[st.Name] = st
	}
	out := make([]MCPServer, len(servers))
	for i, s := range servers {
		out[i] = MCPServer{MCPServerConfig: mcpServerConfig(s)}
		if st, ok := byName[s.Name]; ok {
			out[i].Status = &st
		}
	}
	return MCPServerListResult{Data: out}, nil, nil
}

func (c *conn) mcpServerSet(ctx context.Context, p MCPServerConfig) (MCPServerConfig, func(), error) {
	s := user.MCPServer{Name: p.Name, Command: p.Command, Args: p.Args, Env: p.Env, URL: p.URL, Headers: p.Headers}
	return p, nil, c.s.app.SetMCPServer(ctx, c.user, s)
}

func (c *conn) mcpServerDelete(ctx context.Context, p MCPServerDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.app.DeleteMCPServer(ctx, c.user, p.Name)
}

func (c *conn) workspaceList(ctx context.Context, _ Empty) (WorkspaceListResult, func(), error) {
	ws, err := c.s.app.Workspaces(ctx, c.user)
	return WorkspaceListResult{Data: each(ws, workspaceRow)}, nil, err
}

func (c *conn) workspaceAdd(ctx context.Context, p WorkspaceAddParams) (WorkspaceResult, func(), error) {
	w, err := c.s.app.AddWorkspace(ctx, c.user, p.Path, p.Name)
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}

func (c *conn) workspaceRename(ctx context.Context, p WorkspaceRenameParams) (WorkspaceResult, func(), error) {
	w, err := c.s.app.RenameWorkspace(ctx, c.user, p.WorkspaceID, p.Name)
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}

func (c *conn) workspaceMove(ctx context.Context, p WorkspaceMoveParams) (WorkspaceResult, func(), error) {
	w, err := c.s.app.MoveWorkspace(ctx, c.user, p.WorkspaceID, p.Path)
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}

func (c *conn) workspaceArchive(ctx context.Context, p WorkspaceArchiveParams) (WorkspaceResult, func(), error) {
	if p.Archived == nil {
		return WorkspaceResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "archived is required")
	}
	w, err := c.s.app.ArchiveWorkspace(ctx, c.user, p.WorkspaceID, *p.Archived)
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}

func (c *conn) workspacePermissions(ctx context.Context, p WorkspacePermissionsParams) (WorkspaceResult, func(), error) {
	w, err := c.s.app.UpdateWorkspacePermissions(ctx, c.user, p.WorkspaceID, p.Settings)
	return WorkspaceResult{Workspace: workspaceRow(w)}, nil, err
}

func (c *conn) environmentList(ctx context.Context, _ Empty) (EnvironmentListResult, func(), error) {
	envs, err := c.s.app.Environments(ctx, c.user)
	return EnvironmentListResult{Data: each(envs, environmentRow)}, nil, err
}

func (c *conn) environmentAdd(ctx context.Context, p EnvironmentAddParams) (EnvironmentResult, func(), error) {
	e, err := c.s.app.AddEnvironment(ctx, c.user, p.Name, p.Kind)
	return EnvironmentResult{Environment: environmentRow(e)}, nil, err
}

func (c *conn) environmentRename(ctx context.Context, p EnvironmentRenameParams) (EnvironmentResult, func(), error) {
	e, err := c.s.app.RenameEnvironment(ctx, c.user, p.EnvironmentID, p.Name)
	return EnvironmentResult{Environment: environmentRow(e)}, nil, err
}

func (c *conn) environmentDelete(ctx context.Context, p EnvironmentDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.app.DeleteEnvironment(ctx, c.user, p.EnvironmentID)
}

func (c *conn) cronList(ctx context.Context, p CronListParams) (CronListResult, func(), error) {
	jobs, err := c.s.app.CronJobs(ctx, c.user, p.ThreadID)
	return CronListResult{Data: each(jobs, cronJobRow)}, nil, err
}

func (c *conn) cronCreate(ctx context.Context, p CronCreateParams) (CronResult, func(), error) {
	j, err := c.s.app.CreateCronJob(ctx, c.user, p.ThreadID, p.Name, p.Spec, p.Prompt, p.Times)
	return CronResult{Job: cronJobRow(j)}, nil, err
}

func (c *conn) cronPause(ctx context.Context, p CronPauseParams) (CronResult, func(), error) {
	if p.Paused == nil {
		return CronResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
	}
	j, err := c.s.app.PauseCronJob(ctx, c.user, p.ThreadID, p.Name, *p.Paused)
	return CronResult{Job: cronJobRow(j)}, nil, err
}

func (c *conn) cronDelete(ctx context.Context, p CronDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.app.DeleteCronJob(ctx, c.user, p.ThreadID, p.Name)
}

func (c *conn) memoryList(ctx context.Context, p MemoryListParams) (MemoryListResult, func(), error) {
	ms, err := c.s.app.Memories(ctx, c.user, memory.Place{Scope: p.Scope, Workspace: p.WorkspaceID})
	return MemoryListResult{Data: each(ms, memoryRow)}, nil, err
}

func (c *conn) memoryStatus(ctx context.Context, _ Empty) (MemoryStatus, func(), error) {
	s, err := c.s.app.MemoryStatus(ctx, c.user)
	if err != nil {
		return MemoryStatus{}, nil, err
	}
	settings, err := c.s.app.Settings(ctx, c.user)
	return MemoryStatus{Enabled: settings.Memory, Unread: s.Unread, Active: s.Active, Attempts: s.Attempts, NextAttempt: s.NextAttempt,
		Skipped: s.Skipped, LastSuccess: s.LastSuccess, LastFailure: s.LastFailure, LastSkip: s.LastSkip}, nil, err
}

func (c *conn) memoryUpdate(ctx context.Context, p MemoryUpdateParams) (MemoryResult, func(), error) {
	m, err := c.s.app.EditMemory(ctx, c.user, p.ID, p.Text)
	return MemoryResult{Memory: memoryRow(m)}, nil, err
}

func (c *conn) memoryMove(ctx context.Context, p MemoryMoveParams) (MemoryResult, func(), error) {
	m, err := c.s.app.MoveMemory(ctx, c.user, p.ID)
	return MemoryResult{Memory: memoryRow(m)}, nil, err
}

func (c *conn) memoryForget(ctx context.Context, p MemoryForgetParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.app.ForgetMemory(ctx, c.user, p.ID)
}

func (c *conn) search(ctx context.Context, p SearchParams) (SearchResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	f, notes, err := c.s.app.Search(ctx, c.user, p.Query, p.WorkspaceID, p.Limit)
	return SearchResult{Found: found(f), Memories: each(notes, memoryRow)}, nil, err
}

func (c *conn) settingsRead(ctx context.Context, _ Empty) (SettingsRow, func(), error) {
	s, err := c.s.app.Settings(ctx, c.user)
	return settingsRow(s), nil, err
}

func (c *conn) settingsUpdate(ctx context.Context, p SettingsUpdateParams) (SettingsRow, func(), error) {
	if p.Redaction == nil && p.Memory == nil {
		return SettingsRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "give redaction or memory")
	}
	s, err := c.s.app.UpdateSettings(ctx, c.user, p.Redaction, p.Memory)
	return settingsRow(s), nil, err
}

func (c *conn) settingsPermissions(ctx context.Context, p SettingsPermissionsParams) (SettingsRow, func(), error) {
	s, err := c.s.app.UpdateUserPermissions(ctx, c.user, p.Settings)
	return settingsRow(s), nil, err
}

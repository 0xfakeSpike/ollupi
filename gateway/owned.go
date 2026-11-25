package gateway

import (
	"context"
	"github.com/0xfakeSpike/ollupi/pkg/llm"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
)

func (c *conn) modelList(ctx context.Context, _ Empty) (ModelListResult, error) {
	models, first, seq, err := c.s.app.Models(ctx, c.user)
	if models == nil {
		// A deployment serving no models yet lists none, not null.
		models = []llm.Model{}
	}
	return ModelListResult{Data: models, Default: first, Seq: seq}, err
}

func (c *conn) catalogRead(ctx context.Context, _ Empty) (CatalogResult, error) {
	providers, seq, err := c.s.app.Catalog(ctx)
	return CatalogResult{Data: each(providers, providerRow), Seq: seq}, err
}

func (c *conn) catalogSet(ctx context.Context, p CatalogSetParams) (Empty, error) {
	providers := make([]account.ModelProvider, len(p.Data))
	for i, row := range p.Data {
		providers[i] = account.ModelProvider{ID: row.ID, API: row.API, BaseURL: row.BaseURL, APIKey: row.APIKey, Models: row.Models}
	}
	return Empty{}, c.s.app.SetCatalog(ctx, providers)
}

func (c *conn) userRead(ctx context.Context, _ Empty) (UserResult, error) {
	u, err := c.s.app.User(ctx, c.user)
	return UserResult{User: userRow(u)}, err
}

func (c *conn) userList(ctx context.Context, _ Empty) (UserListResult, error) {
	users, seq, err := c.s.app.Users(ctx)
	return UserListResult{Data: each(users, userRow), Seq: seq}, err
}

func (c *conn) userCreate(ctx context.Context, p UserCreateParams) (UserResult, error) {
	u, err := c.s.app.CreateUser(ctx, p.Name)
	if err != nil {
		return UserResult{}, err
	}
	if c.s.welcome != nil {
		if err := c.s.welcome(ctx, u.ID); err != nil {
			return UserResult{}, err
		}
	}
	return UserResult{User: userRow(u)}, nil
}

// userUpdate renames the connection's own user, or, for the operator, any.
func (c *conn) userUpdate(ctx context.Context, p UserUpdateParams) (UserResult, error) {
	if p.ID != c.user && !c.operator {
		return UserResult{}, jsonrpc.Errorf(errForbidden, "a user renames only themself; another is the deployment operator's to rename")
	}
	if _, err := c.s.app.User(ctx, p.ID); err != nil {
		return UserResult{}, err
	}
	u, err := c.s.app.RenameUser(ctx, p.ID, p.Name)
	return UserResult{User: userRow(u)}, err
}

func userRow(u account.User) UserRow { return UserRow{ID: u.ID, Name: u.Name} }

func providerRow(p account.ModelProvider) ModelProviderRow {
	models := p.Models
	if models == nil {
		models = []account.ProviderModel{}
	}
	return ModelProviderRow{ID: p.ID, API: p.API, BaseURL: p.BaseURL, APIKey: p.APIKey, Models: models}
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
	servers, seq, err := c.s.app.MCPServers(ctx, c.user)
	out := make([]MCPServer, len(servers))
	for i, s := range servers {
		out[i] = MCPServer{MCPServerConfig: mcpServerConfig(s.MCPServer)}
		if st := s.Status; st != nil {
			out[i].Status = &MCPServerStatus{Name: st.Name, Connected: st.Connected, Tools: st.Tools, SkippedTools: st.Skipped, Error: st.Error}
		}
	}
	return MCPServerListResult{Data: out, Seq: seq}, err
}

func (c *conn) mcpServerSet(ctx context.Context, p MCPServerConfig) (MCPServerConfig, error) {
	s := account.MCPServer{Name: p.Name, Command: p.Command, Args: p.Args, Env: p.Env, URL: p.URL, Headers: p.Headers}
	return p, c.s.app.SetMCPServer(ctx, c.user, s)
}

func (c *conn) mcpServerDelete(ctx context.Context, p MCPServerDeleteParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteMCPServer(ctx, c.user, p.Name)
}

func (c *conn) workspaceList(ctx context.Context, _ Empty) (WorkspaceListResult, error) {
	ws, seq, err := c.s.app.Workspaces(ctx, c.user)
	return WorkspaceListResult{Data: each(ws, workspaceRow), Seq: seq}, err
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

func (c *conn) workspaceSetKind(ctx context.Context, p WorkspaceSetKindParams) (WorkspaceResult, error) {
	w, err := c.s.app.SetWorkspaceKind(ctx, c.user, p.WorkspaceID, p.Kind)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) workspacePermissions(ctx context.Context, p WorkspacePermissionsParams) (WorkspaceResult, error) {
	w, err := c.s.app.UpdateWorkspacePermissions(ctx, c.user, p.WorkspaceID, p.Settings)
	return WorkspaceResult{Workspace: workspaceRow(w)}, err
}

func (c *conn) environmentList(ctx context.Context, _ Empty) (EnvironmentListResult, error) {
	envs, seq, err := c.s.app.Environments(ctx, c.user)
	return EnvironmentListResult{Data: each(envs, environmentRow), Seq: seq}, err
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
	jobs, seq, err := c.s.app.CronJobs(ctx, c.user, p.ThreadID)
	return CronListResult{Data: each(jobs, cronJobRow), Seq: seq}, err
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

const (
	todoDefaultLimit = 500
	todoMaxLimit     = 2000
)

// todoList lists the todos still open and the ones handed out, unless other statuses are asked for.
func (c *conn) todoList(ctx context.Context, p TodoListParams) (TodoListResult, error) {
	if len(p.Statuses) == 0 {
		p.Statuses = []account.TodoStatus{account.TodoOpen, account.TodoHanded}
	}
	if err := limit(&p.Limit, todoDefaultLimit, todoMaxLimit); err != nil {
		return TodoListResult{}, err
	}
	todos, more, seq, err := c.s.app.Todos(ctx, c.user, p.Statuses, p.Limit)
	return TodoListResult{Data: each(todos, todoRow), More: more, Seq: seq}, err
}

func (c *conn) todoAdd(ctx context.Context, p TodoAddParams) (TodoResult, error) {
	t, err := c.s.app.AddTodo(ctx, c.user, p.Title, p.Notes)
	return TodoResult{Todo: todoRow(t)}, err
}

func (c *conn) todoUpdate(ctx context.Context, p TodoUpdateParams) (TodoResult, error) {
	t, err := c.s.app.UpdateTodo(ctx, c.user, p.ID, app.TodoChange{
		Title: p.Title, Status: p.Status, Notes: p.Notes, Result: p.Result, Thread: p.ThreadID})
	return TodoResult{Todo: todoRow(t)}, err
}

func (c *conn) memoryList(ctx context.Context, p MemoryListParams) (MemoryListResult, error) {
	ms, seq, err := c.s.app.Memories(ctx, c.user, memory.Place{Scope: p.Scope, Workspace: p.WorkspaceID})
	return MemoryListResult{Data: each(ms, memoryRow), Seq: seq}, err
}

func (c *conn) memoryStatus(ctx context.Context, _ Empty) (MemoryStatus, error) {
	s, err := c.s.app.MemoryStatus(ctx, c.user)
	if err != nil {
		return MemoryStatus{}, err
	}
	settings, _, err := c.s.app.Settings(ctx, c.user)
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

func (c *conn) usageQuery(ctx context.Context, p UsageQueryParams) (UsageQueryResult, error) {
	buckets, err := c.s.app.Usage(ctx, c.user, store.UsageQuery{From: p.From, To: p.To, Group: p.GroupBy,
		OffsetMinutes: p.OffsetMinutes})
	out := make([]UsageRow, len(buckets))
	for i, b := range buckets {
		out[i] = UsageRow{Key: b.Key, Provider: b.Provider, Calls: b.Calls, Usage: b.Tokens}
	}
	return UsageQueryResult{Data: out}, err
}

// presenceRecord writes what the desktop client saw: the stretches of time the
// user spent on a thread, in the app, or away from it.
func (c *conn) presenceRecord(ctx context.Context, p PresenceRecordParams) (Empty, error) {
	rows := make([]store.Presence, len(p.Rows))
	for i, r := range p.Rows {
		rows[i] = store.Presence{ID: r.ID, Thread: r.ThreadID, Todo: r.TodoID, Workspace: r.WorkspaceID,
			State: r.State, Start: r.Start, End: r.End}
	}
	return Empty{}, c.s.app.RecordPresence(ctx, c.user, rows)
}

func (c *conn) presenceSummary(ctx context.Context, p PresenceSummaryParams) (PresenceSummaryResult, error) {
	buckets, err := c.s.app.Presence(ctx, c.user, store.PresenceQuery{From: p.From, To: p.To, Group: p.GroupBy,
		OffsetMinutes: p.OffsetMinutes})
	out := make([]PresenceRow, len(buckets))
	for i, b := range buckets {
		out[i] = PresenceRow{Key: b.Key, Seconds: b.Seconds}
	}
	return PresenceSummaryResult{Data: out}, err
}

func (c *conn) settingsRead(ctx context.Context, _ Empty) (SettingsReadResult, error) {
	s, seq, err := c.s.app.Settings(ctx, c.user)
	return SettingsReadResult{SettingsRow: settingsRow(s), Seq: seq}, err
}

func (c *conn) settingsUpdate(ctx context.Context, p SettingsUpdateParams) (SettingsRow, error) {
	if p.Redaction == nil && p.Memory == nil && p.Learn == nil && p.Model == nil && p.LearnModel == nil {
		return SettingsRow{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "give redaction, memory, learn, model or learnModel")
	}
	s, err := c.s.app.UpdateSettings(ctx, c.user, app.SettingsChange{Redaction: p.Redaction, Memory: p.Memory, Learn: p.Learn, Model: p.Model, LearnModel: p.LearnModel})
	return settingsRow(s), err
}

func (c *conn) settingsPermissions(ctx context.Context, p SettingsPermissionsParams) (SettingsRow, error) {
	s, err := c.s.app.UpdateUserPermissions(ctx, c.user, p.Settings)
	return settingsRow(s), err
}

func (c *conn) skillList(ctx context.Context, _ Empty) (SkillListResult, error) {
	skills, seq, err := c.s.app.Skills(ctx, c.user)
	return SkillListResult{Data: each(skills, skillRow), Seq: seq}, err
}

func (c *conn) skillRead(ctx context.Context, p SkillNameParams) (SkillReadResult, error) {
	s, files, err := c.s.app.Skill(ctx, c.user, p.Name)
	out := SkillReadResult{Skill: skillRow(s), Instructions: s.Instructions, Files: make([]SkillFile, len(files))}
	for i, f := range files {
		out.Files[i] = SkillFile{Path: f.Path, Text: string(f.Body), Executable: f.Executable}
	}
	return out, err
}

func (c *conn) skillSet(ctx context.Context, p SkillSetParams) (SkillResult, error) {
	pkg := skill.Package{Name: p.Name, Description: p.Description, Version: p.Version, Instructions: p.Instructions,
		Roles: make([]string, len(p.Roles)), Files: make([]skill.File, len(p.Files))}
	for i, r := range p.Roles {
		pkg.Roles[i] = string(r)
	}
	for i, f := range p.Files {
		pkg.Files[i] = skill.File{Path: f.Path, Body: []byte(f.Text), Executable: f.Executable}
	}
	s, err := c.s.app.SetSkill(ctx, c.user, pkg)
	return SkillResult{Skill: skillRow(s)}, err
}

func (c *conn) skillDelete(ctx context.Context, p SkillNameParams) (Empty, error) {
	return Empty{}, c.s.app.DeleteSkill(ctx, c.user, p.Name)
}

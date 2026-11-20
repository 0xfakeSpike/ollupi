package gateway

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
)

type ThreadRow struct {
	ID             string                    `json:"id"`
	WorkspaceID    string                    `json:"workspaceId,omitempty"`
	EnvironmentID  string                    `json:"environmentId,omitempty"`
	Title          string                    `json:"title"`
	Settings       policy.Settings           `json:"settings"`
	ParentThreadID string                    `json:"parentThreadId,omitempty"`
	ParentItemID   string                    `json:"parentItemId,omitempty"`
	Role           agent.Name                `json:"role,omitempty"`
	Status         conversation.ThreadStatus `json:"status"`
	CreatedAt      time.Time                 `json:"createdAt"`
	UpdatedAt      time.Time                 `json:"updatedAt"`
	ArchivedAt     *time.Time                `json:"archivedAt,omitempty"`
	RepliedAt      *time.Time                `json:"repliedAt,omitempty"`
	ReadAt         *time.Time                `json:"readAt,omitempty"`
}

func threadRow(t store.Thread) ThreadRow {
	return ThreadRow{
		ID: t.ID, WorkspaceID: t.Workspace, EnvironmentID: t.Environment, Title: t.Title, Settings: t.Settings,
		ParentThreadID: t.Parent, ParentItemID: t.ParentItem, Role: agent.Name(t.Role), Status: t.Status, CreatedAt: t.Created, UpdatedAt: t.Updated,
		ArchivedAt: t.Archived, RepliedAt: t.Replied, ReadAt: t.Read,
	}
}

type TurnStatus string

const (
	TurnQueued      TurnStatus = "queued"
	TurnInProgress  TurnStatus = "inProgress"
	TurnCompleted   TurnStatus = "completed"
	TurnFailed      TurnStatus = "failed"
	TurnInterrupted TurnStatus = "interrupted"
)

var TurnStatuses = []TurnStatus{TurnQueued, TurnInProgress, TurnCompleted, TurnFailed, TurnInterrupted}

type TurnRow struct {
	ID          string             `json:"id"`
	ThreadID    string             `json:"threadId"`
	Status      TurnStatus         `json:"status"`
	Model       llm.Model          `json:"model"`
	Agent       agent.Name         `json:"agent"`
	Usage       conversation.Usage `json:"usage"`
	Responses   []ResponseRow      `json:"responses"`
	Error       string             `json:"error,omitempty"`
	CronJobID   string             `json:"cronJobId,omitempty"`
	QueuedAt    time.Time          `json:"queuedAt"`
	CompletedAt *time.Time         `json:"completedAt,omitempty"`
}

type ResponseRow struct {
	ID           string                    `json:"id"`
	Usage        conversation.Usage        `json:"usage"`
	ContextStats conversation.ContextStats `json:"contextStats"`
}

func turnRow(thread string, t conversation.Turn, running bool) TurnRow {
	row := TurnRow{ID: t.ID, ThreadID: thread, Status: TurnQueued, Model: t.Model, Agent: agent.Name(t.Role), Usage: t.Usage,
		CronJobID: t.CronJob, QueuedAt: t.QueuedAt, Responses: make([]ResponseRow, len(t.Responses))}
	for i, r := range t.Responses {
		row.Responses[i] = ResponseRow{ID: r.Response, Usage: r.Usage, ContextStats: r.Stats}
	}
	switch {
	case t.Ended != nil:
		at := t.Ended.At
		row.Status, row.Error, row.CompletedAt = TurnStatus(t.Ended.Status), t.Ended.Reason, &at
	case running:
		row.Status = TurnInProgress
	}
	return row
}

type Approval struct {
	ApprovalID         string                  `json:"approvalId"`
	Reason             string                  `json:"reason,omitempty"`
	Rule               string                  `json:"rule,omitempty"`
	Content            string                  `json:"content,omitempty"`
	SuggestedRules     []string                `json:"suggestedRules,omitempty"`
	AvailableDecisions []conversation.Decision `json:"availableDecisions"`
	AskedAt            time.Time               `json:"askedAt"`
	Decision           conversation.Decision   `json:"decision,omitempty"`
	DecidedBy          string                  `json:"decidedBy,omitempty"`
	DecidedAt          *time.Time              `json:"decidedAt,omitempty"`
}

type Input struct {
	Request  json.RawMessage `json:"request"`
	AskedAt  time.Time       `json:"askedAt"`
	Answered bool            `json:"answered"`
}

type Result struct {
	Output    json.RawMessage         `json:"output"`
	Artifacts []conversation.Artifact `json:"artifacts,omitempty"`
}

type ItemRow struct {
	ID                string                     `json:"id"`
	TurnID            string                     `json:"turnId"`
	Seq               int64                      `json:"seq"`
	Updated           int64                      `json:"updated"`
	Type              conversation.ItemKind      `json:"type"`
	Status            conversation.ItemStatus    `json:"status"`
	Text              string                     `json:"text,omitempty"`
	Error             string                     `json:"error,omitempty"`
	Response          string                     `json:"responseId,omitempty"`
	CallID            string                     `json:"callId,omitempty"`
	Tool              string                     `json:"tool,omitempty"`
	Server            string                     `json:"server,omitempty"`
	Arguments         string                     `json:"arguments,omitempty"`
	Annotations       json.RawMessage            `json:"annotations,omitempty"`
	PreparedExecution json.RawMessage            `json:"preparedExecution,omitempty"`
	Approval          *Approval                  `json:"approval,omitempty"`
	Input             *Input                     `json:"input,omitempty"`
	Result            *Result                    `json:"result,omitempty"`
	ChildThreadID     string                     `json:"childThreadId,omitempty"`
	Detached          bool                       `json:"detached,omitempty"`
	TaskID            string                     `json:"taskId,omitempty"`
	LaunchItemID      string                     `json:"launchItemId,omitempty"`
	Attachments       []conversation.Attachment  `json:"attachments,omitempty"`
	ContextStats      *conversation.ContextStats `json:"contextStats,omitempty"`
	Memory            []conversation.MemoryRef   `json:"memory,omitempty"`
	StartedAt         time.Time                  `json:"startedAt"`
	CompletedAt       *time.Time                 `json:"completedAt,omitempty"`
}

func itemRow(it conversation.Item) ItemRow {
	row := ItemRow{
		ID: it.ID, TurnID: it.Turn, Seq: it.Seq, Updated: it.Updated, Type: it.Kind, Status: it.Status, Text: it.Text, Error: it.Error,
		Response: it.Response, ChildThreadID: it.Child, Detached: it.Detached, TaskID: it.Task, LaunchItemID: it.Launch, Attachments: it.Attachments, ContextStats: it.Stats,
		Memory: it.Memories, StartedAt: it.StartedAt, CompletedAt: it.CompletedAt,
	}
	if c := it.Call; c != nil {
		row.CallID, row.Tool, row.Server, row.Arguments, row.Annotations, row.PreparedExecution = c.Call, c.Tool, c.Server, c.Arguments, c.Annotations, c.Prepared
	}
	if a := it.Approval; a != nil {
		row.Approval = &Approval{ApprovalID: a.Approval, Reason: a.Reason, Rule: a.Rule, Content: a.Content, SuggestedRules: a.Suggested,
			AvailableDecisions: a.Decisions(), AskedAt: a.AskedAt, Decision: a.Decision, DecidedBy: a.By, DecidedAt: a.DecidedAt}
	}
	if in := it.Input; in != nil {
		row.Input = &Input{Request: in.Request, AskedAt: in.AskedAt, Answered: in.Answered}
	}
	if r := it.Result; r != nil {
		row.Result = &Result{Output: r.Output, Artifacts: r.Artifacts}
	}
	return row
}

type ThreadView struct {
	Thread ThreadRow `json:"thread"`
	Turns  []TurnRow `json:"turns"`
	Items  []ItemRow `json:"items"`
	Seq    int64     `json:"seq"`
}

type WorkspaceRow struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	RootPath     string                `json:"rootPath"`
	Kind         account.WorkspaceKind `json:"kind"`
	Instructions string                `json:"instructions,omitempty"`
	Providers    account.Providers     `json:"providers"`
	Settings     policy.Settings       `json:"settings"`
	CreatedAt    time.Time             `json:"createdAt"`
	UpdatedAt    time.Time             `json:"updatedAt"`
	ArchivedAt   *time.Time            `json:"archivedAt,omitempty"`
}

func workspaceRow(w account.Workspace) WorkspaceRow {
	return WorkspaceRow{ID: w.ID, Name: w.Name, RootPath: w.Root, Kind: w.Kind, Instructions: w.Instructions, Providers: w.Providers,
		Settings: w.Settings, CreatedAt: w.Created, UpdatedAt: w.Updated, ArchivedAt: w.Archived}
}

type EnvironmentRow struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Kind      account.EnvironmentKind `json:"kind"`
	CreatedAt time.Time               `json:"createdAt"`
	UpdatedAt time.Time               `json:"updatedAt"`
}

func environmentRow(e account.Environment) EnvironmentRow {
	return EnvironmentRow{ID: e.ID, Name: e.Name, Kind: e.Kind, CreatedAt: e.Created, UpdatedAt: e.Updated}
}

type CronJobRow struct {
	ID             string     `json:"id"`
	ThreadID       string     `json:"threadId"`
	Name           string     `json:"name"`
	Spec           string     `json:"spec"`
	Prompt         string     `json:"prompt"`
	Times          *int       `json:"times,omitempty"`
	Runs           int        `json:"runs"`
	Paused         bool       `json:"paused"`
	NextRunAt      time.Time  `json:"nextRunAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	ThreadTitle    string     `json:"threadTitle"`
	WorkspaceID    string     `json:"workspaceId,omitempty"`
	ThreadArchived bool       `json:"threadArchived,omitempty"`
	LastTurnID     string     `json:"lastTurnId,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
}

func cronJobRow(j store.CronJobView) CronJobRow {
	return CronJobRow{ID: j.ID, ThreadID: j.Thread, Name: j.Name, Spec: j.Spec, Prompt: j.Prompt, Times: j.Times, Runs: j.Runs,
		Paused: j.Paused, NextRunAt: j.NextRun, CreatedAt: j.Created, ThreadTitle: j.Title, WorkspaceID: j.Workspace,
		ThreadArchived: j.Archived, LastTurnID: j.LastTurn, LastRunAt: j.LastRun}
}

type MemoryRow struct {
	ID                string          `json:"id"`
	Scope             memory.Scope    `json:"scope"`
	WorkspaceID       string          `json:"workspaceId,omitempty"`
	OriginWorkspaceID string          `json:"originWorkspaceId,omitempty"`
	Topic             string          `json:"topic"`
	Kind              memory.Kind     `json:"kind"`
	Text              string          `json:"text"`
	Status            memory.Status   `json:"status"`
	UserEdited        bool            `json:"userEdited,omitempty"`
	Sources           []memory.Source `json:"sources"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

func memoryRow(m memory.Memory) MemoryRow {
	return MemoryRow{ID: m.ID, Scope: m.Scope, WorkspaceID: m.Workspace, OriginWorkspaceID: m.Origin, Topic: m.Topic, Kind: m.Kind,
		Text: m.Text, Status: m.Status, UserEdited: m.UserEdited, Sources: nonNil(m.Sources), CreatedAt: m.Created, UpdatedAt: m.Updated}
}

type MemoryStatus struct {
	Enabled     bool            `json:"enabled"`
	Unread      int64           `json:"unread"`
	Active      int             `json:"active"`
	Attempts    int             `json:"attempts,omitempty"`
	NextAttempt *time.Time      `json:"nextAttempt,omitempty"`
	Skipped     int64           `json:"skipped"`
	LastSuccess *time.Time      `json:"lastSuccess,omitempty"`
	LastFailure *memory.Failure `json:"lastFailure,omitempty"`
	LastSkip    *memory.Skip    `json:"lastSkip,omitempty"`
}

type SettingsRow struct {
	Redaction   bool            `json:"redaction"`
	Memory      bool            `json:"memory"`
	Learn       bool            `json:"learn"`
	Permissions policy.Settings `json:"permissions"`
}

func settingsRow(s account.Settings) SettingsRow {
	return SettingsRow{Redaction: s.Redaction, Memory: s.Memory, Learn: s.Learn, Permissions: s.Permissions}
}

type MCPServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func mcpServerConfig(s account.MCPServer) MCPServerConfig {
	return MCPServerConfig{Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers}
}

type MessageHit struct {
	ThreadID string                `json:"threadId"`
	Title    string                `json:"title"`
	ItemID   string                `json:"itemId"`
	TurnID   string                `json:"turnId"`
	Seq      int64                 `json:"seq"`
	Type     conversation.ItemKind `json:"type"`
	Snippet  string                `json:"snippet"`
	At       time.Time             `json:"at"`
}

type Found struct {
	Threads  []ThreadRow  `json:"threads"`
	Messages []MessageHit `json:"messages"`
	CronJobs []CronJobRow `json:"cronJobs"`
}

func found(f store.Found) Found {
	messages := make([]MessageHit, len(f.Messages))
	for i, h := range f.Messages {
		messages[i] = MessageHit{ThreadID: h.Thread, Title: h.Title, ItemID: h.Item, TurnID: h.Turn, Seq: h.Seq, Type: h.Kind, Snippet: h.Snippet, At: h.At}
	}
	return Found{Threads: each(f.Threads, threadRow), Messages: messages, CronJobs: each(f.CronJobs, cronJobRow)}
}

func each[T, U any](rows []T, convert func(T) U) []U {
	out := make([]U, len(rows))
	for i, r := range rows {
		out[i] = convert(r)
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

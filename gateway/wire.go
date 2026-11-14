package gateway

import (
	"time"

	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// The protocol's own records. They carry what a client reads of the rows the orchestrator keeps, by the names the schema has
// always given them, so that a column the storage adds does not reach clients unless it is added here.

type ThreadRow struct {
	ID             string                  `json:"id"`
	WorkspaceID    repo.Ref                `json:"workspaceId,omitempty"`
	EnvironmentID  repo.Ref                `json:"environmentId,omitempty"`
	Title          string                  `json:"title"`
	Settings       repo.PermissionSettings `json:"settings"`
	ParentThreadID repo.Ref                `json:"parentThreadId,omitempty"`
	ParentItemID   repo.Ref                `json:"parentItemId,omitempty"`
	CreatedAt      time.Time               `json:"createdAt"`
	UpdatedAt      time.Time               `json:"updatedAt"`
	ArchivedAt     *time.Time              `json:"archivedAt,omitempty"`
	RepliedAt      *time.Time              `json:"repliedAt,omitempty"`
	ReadAt         *time.Time              `json:"readAt,omitempty"`
}

func threadRow(t repo.ThreadRow) ThreadRow {
	return ThreadRow{
		ID: t.ID, WorkspaceID: t.WorkspaceID, EnvironmentID: t.EnvironmentID, Title: t.Title, Settings: t.Settings,
		ParentThreadID: t.ParentThreadID, ParentItemID: t.ParentItemID, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		ArchivedAt: t.ArchivedAt, RepliedAt: t.RepliedAt, ReadAt: t.ReadAt,
	}
}

type ThreadSummary struct {
	ThreadRow
	Status repo.ThreadStatus `json:"status"`
}

func threadSummary(t repo.ThreadSummary) ThreadSummary {
	return ThreadSummary{ThreadRow: threadRow(t.ThreadRow), Status: t.Status}
}

type TurnRow struct {
	ID          string            `json:"id"`
	ThreadID    string            `json:"threadId"`
	Seq         int               `json:"seq"`
	Status      repo.TurnStatus   `json:"status"`
	Model       llm.Model         `json:"model"`
	Agent       role.Name         `json:"agent"`
	Usage       repo.Usage        `json:"usage"`
	Error       string            `json:"error,omitempty"`
	CronJobID   string            `json:"cronJobId,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	StartedAt   time.Time         `json:"startedAt"`
	CompletedAt *time.Time        `json:"completedAt,omitempty"`
	Attachments []repo.Attachment `json:"attachments,omitempty"`
	Input       string            `json:"input,omitempty"`
}

func turnRow(t repo.TurnRow) TurnRow {
	return TurnRow{
		ID: t.ID, ThreadID: t.ThreadID, Seq: t.Seq, Status: t.Status, Model: t.Model, Agent: role.Name(t.Agent), Usage: t.Usage, Error: t.Error,
		CronJobID: t.CronJobID, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, CompletedAt: t.CompletedAt,
		Attachments: t.Attachments, Input: t.Input,
	}
}

type ItemRow struct {
	ID                string                 `json:"id"`
	TurnID            string                 `json:"turnId"`
	Seq               int                    `json:"seq"`
	Type              repo.ItemType          `json:"type"`
	Status            repo.ItemStatus        `json:"status"`
	CallID            string                 `json:"callId,omitempty"`
	Tool              string                 `json:"tool,omitempty"`
	Server            string                 `json:"server,omitempty"`
	ResponseID        string                 `json:"responseId,omitempty"`
	Error             string                 `json:"error,omitempty"`
	StartedAt         time.Time              `json:"startedAt"`
	CompletedAt       *time.Time             `json:"completedAt,omitempty"`
	Approval          *repo.Approval         `json:"approval,omitempty"`
	Await             *tools.Await           `json:"await,omitempty"`
	Annotations       *tools.Annotations     `json:"annotations,omitempty"`
	Usage             *repo.Usage            `json:"usage,omitempty"`
	ContextStats      *repo.ContextStats     `json:"contextStats,omitempty"`
	Memory            []repo.MemoryReference `json:"memory,omitempty"`
	Attachments       []repo.Attachment      `json:"attachments,omitempty"`
	BackgroundID      string                 `json:"backgroundId,omitempty"`
	Text              string                 `json:"text,omitempty"`
	Arguments         string                 `json:"arguments,omitempty"`
	PreparedExecution *tools.Prepared        `json:"preparedExecution,omitempty"`
	Result            *repo.ToolResult       `json:"result,omitempty"`
}

func itemRow(it repo.ItemRow) ItemRow {
	return ItemRow{
		ID: it.ID, TurnID: it.TurnID, Seq: it.Seq, Type: it.Type, Status: it.Status, CallID: it.CallID, Tool: it.Tool,
		Server: it.Server, ResponseID: it.ResponseID, Error: it.Error, StartedAt: it.StartedAt, CompletedAt: it.CompletedAt,
		Approval: it.Approval, Await: it.Await, Annotations: it.Annotations, Usage: it.Usage, ContextStats: it.ContextStats,
		Memory: it.Memory, Attachments: it.Attachments, BackgroundID: it.BackgroundID, Text: it.Text, Arguments: it.Arguments,
		PreparedExecution: it.PreparedExecution, Result: it.Result,
	}
}

type ThreadView struct {
	Thread ThreadRow `json:"thread"`
	Turns  []TurnRow `json:"turns"`
	Items  []ItemRow `json:"items"`
}

func threadView(v repo.ThreadView) ThreadView {
	return ThreadView{Thread: threadRow(v.Thread), Turns: each(v.Turns, turnRow), Items: each(v.Items, itemRow)}
}

type WorkspaceRow struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	RootPath   string                  `json:"rootPath"`
	Settings   repo.PermissionSettings `json:"settings"`
	CreatedAt  time.Time               `json:"createdAt"`
	UpdatedAt  time.Time               `json:"updatedAt"`
	ArchivedAt *time.Time              `json:"archivedAt,omitempty"`
}

func workspaceRow(w repo.WorkspaceRow) WorkspaceRow {
	return WorkspaceRow{ID: w.ID, Name: w.Name, RootPath: w.RootPath, Settings: w.Settings, CreatedAt: w.CreatedAt,
		UpdatedAt: w.UpdatedAt, ArchivedAt: w.ArchivedAt}
}

type EnvironmentRow struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Kind      repo.EnvironmentKind `json:"kind"`
	CreatedAt time.Time            `json:"createdAt"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

func environmentRow(e repo.EnvironmentRow) EnvironmentRow {
	return EnvironmentRow{ID: e.ID, Name: e.Name, Kind: e.Kind, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
}

type CronJobRow struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"threadId"`
	Name      string    `json:"name"`
	Spec      string    `json:"spec"`
	Prompt    string    `json:"prompt"`
	Times     *int      `json:"times,omitempty"`
	Runs      int       `json:"runs"`
	Paused    bool      `json:"paused"`
	NextRunAt time.Time `json:"nextRunAt"`
	CreatedAt time.Time `json:"createdAt"`
}

func cronJobRow(j repo.CronJobRow) CronJobRow {
	return CronJobRow{ID: j.ID, ThreadID: j.ThreadID, Name: j.Name, Spec: j.Spec, Prompt: j.Prompt, Times: j.Times,
		Runs: j.Runs, Paused: j.Paused, NextRunAt: j.NextRunAt, CreatedAt: j.CreatedAt}
}

type CronJobView struct {
	CronJobRow
	ThreadTitle    string     `json:"threadTitle"`
	WorkspaceID    string     `json:"workspaceId,omitempty"`
	ParentThreadID string     `json:"parentThreadId,omitempty"`
	ThreadArchived bool       `json:"threadArchived,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	LastTurnID     string     `json:"lastTurnId,omitempty"`
}

func cronJobView(j repo.CronJobView) CronJobView {
	return CronJobView{CronJobRow: cronJobRow(j.CronJobRow), ThreadTitle: j.ThreadTitle, WorkspaceID: j.WorkspaceID,
		ParentThreadID: j.ParentThreadID, ThreadArchived: j.ThreadArchived, LastRunAt: j.LastRunAt, LastTurnID: j.LastTurnID}
}

type MemoryRow struct {
	ID                string               `json:"id"`
	Scope             repo.MemoryScope     `json:"scope"`
	WorkspaceID       string               `json:"workspaceId,omitempty"`
	OriginWorkspaceID string               `json:"originWorkspaceId,omitempty"`
	Topic             string               `json:"topic"`
	Kind              string               `json:"kind"`
	Text              string               `json:"text"`
	Status            repo.MemoryRowStatus `json:"status"`
	UserEdited        bool                 `json:"userEdited,omitempty"`
	Sources           []repo.MemorySource  `json:"sources"`
	CreatedAt         time.Time            `json:"createdAt"`
	UpdatedAt         time.Time            `json:"updatedAt"`
}

func memoryRow(m repo.MemoryRow) MemoryRow {
	return MemoryRow{ID: m.ID, Scope: m.Scope, WorkspaceID: m.WorkspaceID, OriginWorkspaceID: m.OriginWorkspaceID,
		Topic: m.Topic, Kind: m.Kind, Text: m.Text, Status: m.Status, UserEdited: m.UserEdited, Sources: m.Sources,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

type MemoryStateRow struct {
	Attempts    int                 `json:"attempts,omitempty"`
	NextAttempt *time.Time          `json:"nextAttempt,omitempty"`
	Skipped     int64               `json:"skipped"`
	LastSuccess *time.Time          `json:"lastSuccess,omitempty"`
	LastFailure *repo.MemoryFailure `json:"lastFailure,omitempty"`
	LastSkip    *repo.MemorySkip    `json:"lastSkip,omitempty"`
	LastReport  *repo.MemoryReport  `json:"lastReport,omitempty"`
}

type MemoryStatus struct {
	GenerationEnabled bool  `json:"generationEnabled"`
	UseEnabled        bool  `json:"useEnabled"`
	Pending           int64 `json:"pending"`
	Active            int64 `json:"active"`
	MemoryStateRow
}

func memoryStatus(s repo.MemoryStatus) MemoryStatus {
	st := s.MemoryStateRow
	return MemoryStatus{
		GenerationEnabled: s.GenerationEnabled, UseEnabled: s.UseEnabled, Pending: s.Pending, Active: s.Active,
		MemoryStateRow: MemoryStateRow{Attempts: st.Attempts, NextAttempt: st.NextAttempt, Skipped: st.Skipped,
			LastSuccess: st.LastSuccess, LastFailure: st.LastFailure, LastSkip: st.LastSkip, LastReport: st.LastReport},
	}
}

type SettingsRow struct {
	Redaction bool `json:"redaction"`
}

func settingsRow(s repo.SettingsRow) SettingsRow { return SettingsRow{Redaction: s.Redaction} }

type Found struct {
	Threads  []ThreadRow       `json:"threads"`
	Messages []repo.MessageHit `json:"messages"`
	Tasks    []CronJobView     `json:"tasks"`
}

func found(f repo.Found) Found {
	return Found{Threads: each(f.Threads, threadRow), Messages: nonNil(f.Messages), Tasks: each(f.Tasks, cronJobView)}
}

// each converts rows for the wire, and gives a list that is never null.
func each[T, U any](rows []T, convert func(T) U) []U {
	out := make([]U, len(rows))
	for i, r := range rows {
		out[i] = convert(r)
	}
	return out
}

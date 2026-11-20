package conversation

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

type Entry struct {
	Seq  int64
	Turn string
	Item string
	At   time.Time
	Body Body
}

type Body interface{ kind() EntryKind }

type EntryKind string

const (
	KindTurnQueued     EntryKind = "turnQueued"
	KindTurnEnded      EntryKind = "turnEnded"
	KindUserMessage    EntryKind = "userMessage"
	KindAgentMessage   EntryKind = "agentMessage"
	KindReasoning      EntryKind = "reasoning"
	KindModelResponded EntryKind = "modelResponded"
	KindModelFailed    EntryKind = "modelFailed"
	KindToolCall       EntryKind = "toolCall"
	KindApprovalAsked  EntryKind = "approvalAsked"
	KindDecided        EntryKind = "decided"
	KindToolStarted    EntryKind = "toolStarted"
	KindInputAsked     EntryKind = "inputAsked"
	KindAnswered       EntryKind = "answered"
	KindToolResult     EntryKind = "toolResult"
	KindChildLaunched  EntryKind = "childLaunched"
	KindChildReported  EntryKind = "childReported"
	KindTaskLaunched   EntryKind = "taskLaunched"
	KindTaskReported   EntryKind = "taskReported"
	KindCompacted      EntryKind = "compacted"
	KindRecalled       EntryKind = "recalled"
	KindNotice         EntryKind = "notice"
)

func KindOf(b Body) EntryKind { return b.kind() }

type TurnQueued struct {
	Model   llm.Model
	Role    string
	CronJob string
}

type TurnEnded struct {
	Status End
	Reason string
}

type End string

const (
	Completed   End = "completed"
	Failed      End = "failed"
	Interrupted End = "interrupted"
)

var Ends = []End{Completed, Failed, Interrupted}

type UserMessage struct {
	Text        string
	Attachments []Attachment
}

type AgentMessage struct {
	Text     string
	Response string
}

type Reasoning struct {
	Text     string
	Response string
	Started  time.Time
	Ended    time.Time
}

type ModelResponded struct {
	Response   string
	Usage      Usage
	Stats      ContextStats
	Native     []byte
	Compaction bool
}

type ModelFailed struct {
	Error string
}

type ToolCall struct {
	Call        string
	Tool        string
	Server      string
	Arguments   string
	Response    string
	Annotations json.RawMessage
	Prepared    json.RawMessage
}

type ApprovalAsked struct {
	Approval  string
	Reason    string
	Rule      string
	Content   string
	Suggested []string
}

type Decided struct {
	Decision Decision
	By       string
}

type ToolStarted struct{}

type InputAsked struct {
	Request json.RawMessage
}

type Answered struct {
	Output  json.RawMessage
	Skipped bool
}

type ToolResult struct {
	Output    json.RawMessage
	Artifacts []Artifact
	Error     string
	Declined  bool
}

type ChildLaunched struct {
	Child    string
	Detached bool
}

type ChildReported struct {
	Launch string
	Child  string
	Task   string
	Output json.RawMessage
	Error  string
}

type TaskLaunched struct {
	Task string
}

type TaskReported struct {
	Launch string
	Task   string
	Output json.RawMessage
	Error  string
}

type Compacted struct {
	Summary  string
	Through  int64
	Response string
	Stats    ContextStats
}

type Recalled struct {
	Text     string
	Memories []MemoryRef
}

type Notice struct {
	Text string
}

func (TurnQueued) kind() EntryKind     { return KindTurnQueued }
func (TurnEnded) kind() EntryKind      { return KindTurnEnded }
func (UserMessage) kind() EntryKind    { return KindUserMessage }
func (AgentMessage) kind() EntryKind   { return KindAgentMessage }
func (Reasoning) kind() EntryKind      { return KindReasoning }
func (ModelResponded) kind() EntryKind { return KindModelResponded }
func (ModelFailed) kind() EntryKind    { return KindModelFailed }
func (ToolCall) kind() EntryKind       { return KindToolCall }
func (ApprovalAsked) kind() EntryKind  { return KindApprovalAsked }
func (Decided) kind() EntryKind        { return KindDecided }
func (ToolStarted) kind() EntryKind    { return KindToolStarted }
func (InputAsked) kind() EntryKind     { return KindInputAsked }
func (Answered) kind() EntryKind       { return KindAnswered }
func (ToolResult) kind() EntryKind     { return KindToolResult }
func (ChildLaunched) kind() EntryKind  { return KindChildLaunched }
func (ChildReported) kind() EntryKind  { return KindChildReported }
func (TaskLaunched) kind() EntryKind   { return KindTaskLaunched }
func (TaskReported) kind() EntryKind   { return KindTaskReported }
func (Compacted) kind() EntryKind      { return KindCompacted }
func (Recalled) kind() EntryKind       { return KindRecalled }
func (Notice) kind() EntryKind         { return KindNotice }

type Decision string

const (
	Accept           Decision = "accept"
	AcceptForSession Decision = "acceptForSession"
	AcceptAlways     Decision = "acceptAlways"
	Decline          Decision = "decline"
)

var Decisions = []Decision{Accept, AcceptForSession, AcceptAlways, Decline}

type Attachment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	File     string `json:"file"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Tokens   int    `json:"tokens"`
}

type Artifact struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Usage struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
}

func (u Usage) Plus(v Usage) Usage {
	return Usage{u.InputTokens + v.InputTokens, u.CachedInputTokens + v.CachedInputTokens,
		u.CacheWriteInputTokens + v.CacheWriteInputTokens, u.OutputTokens + v.OutputTokens}
}

type ContextStats struct {
	Model            string       `json:"model,omitempty"`
	BudgetTokens     int          `json:"budgetTokens"`
	CompactAtTokens  int          `json:"compactAtTokens"`
	SystemTokens     int          `json:"systemTokens"`
	MessagesTokens   int          `json:"messagesTokens"`
	MemoryTokens     int          `json:"memoryTokens,omitempty"`
	GlobalMemory     *MemoryStats `json:"globalMemory,omitempty"`
	LocalMemory      *MemoryStats `json:"localMemory,omitempty"`
	ToolsTokens      int          `json:"toolsTokens"`
	EstimatedTokens  int          `json:"estimatedTokens"`
	CalibratedTokens int          `json:"calibratedTokens,omitempty"`
}

type MemoryStats struct {
	Tokens int      `json:"tokens"`
	IDs    []string `json:"ids"`
}

const GlobalScope = "global"

type MemoryRef struct {
	ID          string `json:"id"`
	Topic       string `json:"topic,omitempty"`
	Text        string `json:"text"`
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

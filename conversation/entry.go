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

type Body interface{ kind() Kind }

type Kind string

const (
	KindTurnQueued     Kind = "turnQueued"
	KindTurnEnded      Kind = "turnEnded"
	KindUserMessage    Kind = "userMessage"
	KindAgentMessage   Kind = "agentMessage"
	KindReasoning      Kind = "reasoning"
	KindModelResponded Kind = "modelResponded"
	KindModelFailed    Kind = "modelFailed"
	KindToolCall       Kind = "toolCall"
	KindApprovalAsked  Kind = "approvalAsked"
	KindDecided        Kind = "decided"
	KindToolStarted    Kind = "toolStarted"
	KindInputAsked     Kind = "inputAsked"
	KindAnswered       Kind = "answered"
	KindToolResult     Kind = "toolResult"
	KindChildLaunched  Kind = "childLaunched"
	KindChildReported  Kind = "childReported"
	KindCompacted      Kind = "compacted"
	KindRecalled       Kind = "recalled"
	KindNotice         Kind = "notice"
)

func KindOf(b Body) Kind { return b.kind() }

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
	Child string
}

type ChildReported struct {
	Launch string
	Child  string
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

func (TurnQueued) kind() Kind     { return KindTurnQueued }
func (TurnEnded) kind() Kind      { return KindTurnEnded }
func (UserMessage) kind() Kind    { return KindUserMessage }
func (AgentMessage) kind() Kind   { return KindAgentMessage }
func (Reasoning) kind() Kind      { return KindReasoning }
func (ModelResponded) kind() Kind { return KindModelResponded }
func (ModelFailed) kind() Kind    { return KindModelFailed }
func (ToolCall) kind() Kind       { return KindToolCall }
func (ApprovalAsked) kind() Kind  { return KindApprovalAsked }
func (Decided) kind() Kind        { return KindDecided }
func (ToolStarted) kind() Kind    { return KindToolStarted }
func (InputAsked) kind() Kind     { return KindInputAsked }
func (Answered) kind() Kind       { return KindAnswered }
func (ToolResult) kind() Kind     { return KindToolResult }
func (ChildLaunched) kind() Kind  { return KindChildLaunched }
func (ChildReported) kind() Kind  { return KindChildReported }
func (Compacted) kind() Kind      { return KindCompacted }
func (Recalled) kind() Kind       { return KindRecalled }
func (Notice) kind() Kind         { return KindNotice }

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

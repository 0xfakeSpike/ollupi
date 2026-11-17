// Package conversation is a conversation as what it is: a log that only grows. Every fact about a conversation is
// an entry appended to it, and every state of it, its turns, its items, what it waits on, is folded from the entries.
// The package does no I/O; a store keeps the entries and the views folded from them.
package conversation

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// Seq orders the entries of one conversation; the store gives each the next one as it appends it.
type Entry struct {
	Seq  int64
	Turn string
	// Item is the item the entry is part of, "" for a turn's own entries.
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
)

func KindOf(b Body) Kind { return b.kind() }

// A turn waits behind the conversation's turn in progress until that one ends.
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

type UserMessage struct {
	Text        string
	Attachments []Attachment
}

// Response names the model response the text came in.
type AgentMessage struct {
	Text     string
	Response string
}

type Reasoning struct {
	Text     string
	Response string
}

// Native is the provider's own record of the response, replayed as is to the same kind of provider.
type ModelResponded struct {
	Response string
	Usage    Usage
	Stats    ContextStats
	Native   []byte
}

type ModelFailed struct {
	Error string
}

// Annotations and Prepared are the tool's, kept as the tool gave them.
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

// The call waits on the user for as long as its questions go unanswered.
type InputAsked struct {
	Request json.RawMessage
}

type Answered struct {
	Output  json.RawMessage
	Skipped bool
}

// One of Output, Error or Declined.
type ToolResult struct {
	Output    json.RawMessage
	Artifacts []Artifact
	Error     string
	Declined  bool
}

// The call launched a subagent in a conversation of its own, whose report the turn waits for.
type ChildLaunched struct {
	Child string
}

// A report is an item of its own, of the turn that launched the subagent.
type ChildReported struct {
	Launch string
	Child  string
	Task   string
	Output json.RawMessage
	Error  string
}

// The summary stands for every entry up to Through in what the model is sent.
type Compacted struct {
	Summary  string
	Through  int64
	Response string
	Stats    ContextStats
}

type Recalled struct {
	Memories []MemoryRef
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

type Decision string

const (
	Accept           Decision = "accept"
	AcceptForSession Decision = "acceptForSession"
	AcceptAlways     Decision = "acceptAlways"
	Decline          Decision = "decline"
)

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
	InputTokens       int64 `json:"inputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`
	// Part of InputTokens.
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
}

func (u Usage) Plus(v Usage) Usage {
	return Usage{u.InputTokens + v.InputTokens, u.CachedInputTokens + v.CachedInputTokens,
		u.CacheWriteInputTokens + v.CacheWriteInputTokens, u.OutputTokens + v.OutputTokens}
}

type ContextStats struct {
	Model            string `json:"model,omitempty"`
	BudgetTokens     int    `json:"budgetTokens"`
	CompactAtTokens  int    `json:"compactAtTokens"`
	SystemTokens     int    `json:"systemTokens"`
	MessagesTokens   int    `json:"messagesTokens"`
	MemoryTokens     int    `json:"memoryTokens,omitempty"`
	ToolsTokens      int    `json:"toolsTokens"`
	EstimatedTokens  int    `json:"estimatedTokens"`
	CalibratedTokens int    `json:"calibratedTokens,omitempty"`
}

type MemoryRef struct {
	ID          string `json:"id"`
	Topic       string `json:"topic,omitempty"`
	Text        string `json:"text"`
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

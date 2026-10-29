package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type Client interface {
	Chat(ctx context.Context, req ChatRequest, onDelta func(Delta)) (ChatResponse, error)
	Limits(model string) (Limits, error)
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ChatMessage struct {
	Role             Role          `json:"role"`
	Content          string        `json:"content,omitempty"`
	Parts            []ContentPart `json:"parts,omitempty"`
	ReasoningContent string        `json:"reasoning,omitempty"`
	ToolCalls        []ToolCall    `json:"toolCalls,omitempty"`
	ToolCallID       string        `json:"toolCallId,omitempty"`
	// Native is a provider's own record of an assistant message. Only the client of the same provider reads it,
	// and replays it in place of Content, ReasoningContent and ToolCalls.
	Native []byte `json:"-"`
}

type PartType string

const (
	PartImage PartType = "image"
	PartText  PartType = "text"
)

// ContentPart is an image or a text file sent with a user message after its Content.
// Path names the file to load before sending; Data holds the loaded bytes.
type ContentPart struct {
	Type     PartType `json:"type"`
	Name     string   `json:"name,omitempty"`
	MimeType string   `json:"mimeType,omitempty"`
	Tokens   int      `json:"tokens,omitempty"`
	Text     string   `json:"-"`
	Path     string   `json:"-"`
	Data     []byte   `json:"-"`
}

func (p ContentPart) DataURL() string {
	return "data:" + p.MimeType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
}

func PartTokens(messages []ChatMessage) int {
	n := 0
	for _, m := range messages {
		for _, p := range m.Parts {
			n += p.Tokens
		}
	}
	return n
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ToolChoice string

const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

type Model struct {
	Name       string `json:"name"`
	Connection string `json:"connection"`
	Thinking   *bool  `json:"thinking,omitempty"`
	Images     bool   `json:"images,omitempty"`
}

func (m Model) Clone() Model {
	if m.Thinking != nil {
		t := *m.Thinking
		m.Thinking = &t
	}
	return m
}

type ChatRequest struct {
	Model             Model
	Messages          []ChatMessage
	Tools             []ToolSpec
	ToolChoice        ToolChoice
	ParallelToolCalls bool
}

type Usage struct {
	PromptTokens       int64
	CachedPromptTokens int64
	// CacheWritePromptTokens counts prompt tokens written to the provider's cache; they are part of PromptTokens.
	CacheWritePromptTokens int64
	CompletionTokens       int64
}

type ChatResponse struct {
	Message       ChatMessage
	FinishReason  string
	Usage         Usage
	UsageReported bool
}

type Delta struct {
	Content   string
	Reasoning string
}

// native is the envelope of ChatMessage.Native: the provider that wrote it, its message in the provider's own
// format, and for providers that bind replayed reasoning to the conversation, the hash of what preceded it.
type native struct {
	Provider string          `json:"provider"`
	Message  json.RawMessage `json:"message"`
	Prefix   string          `json:"prefix,omitempty"`
}

func (n native) encode() []byte {
	b, err := json.Marshal(n)
	if err != nil {
		panic(fmt.Sprintf("llm: encode native message: %v", err))
	}
	return b
}

// nativeOf returns the message's native record if provider wrote it.
func nativeOf(m ChatMessage, provider string) (native, bool) {
	var n native
	if len(m.Native) == 0 || json.Unmarshal(m.Native, &n) != nil || n.Provider != provider {
		return native{}, false
	}
	return n, true
}

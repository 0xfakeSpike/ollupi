package llm

import (
	"context"
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
	Native           []byte        `json:"-"`
}

type PartType string

const (
	PartImage PartType = "image"
	PartText  PartType = "text"
)

type ContentPart struct {
	Type     PartType `json:"type"`
	Name     string   `json:"name,omitempty"`
	MimeType string   `json:"mimeType,omitempty"`
	Tokens   int      `json:"tokens,omitempty"`
	Text     string   `json:"-"`
	Path     string   `json:"-"`
	Data     []byte   `json:"-"`
}

func emit(onDelta func(Delta), d Delta) {
	if onDelta != nil && (d.Content != "" || d.Reasoning != "") {
		onDelta(d)
	}
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

func (m Model) thinkingOn() bool { return m.Thinking == nil || *m.Thinking }

func (m Model) Clone() Model {
	if m.Thinking != nil {
		t := *m.Thinking
		m.Thinking = &t
	}
	return m
}

type ChatRequest struct {
	Model       Model
	Messages    []ChatMessage
	Tools       []ToolSpec
	ToolChoice  ToolChoice
	InputTokens int
}

type Usage struct {
	PromptTokens           int64
	CachedPromptTokens     int64
	CacheWritePromptTokens int64
	CompletionTokens       int64
}

type ChatResponse struct {
	Message      ChatMessage
	FinishReason string
	Usage        Usage
}

type Delta struct {
	Content   string
	Reasoning string
}

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

func nativeOf(m ChatMessage, provider string) (native, bool) {
	var n native
	if len(m.Native) == 0 || json.Unmarshal(m.Native, &n) != nil || n.Provider != provider {
		return native{}, false
	}
	return n, true
}

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

const providerAnthropic = "anthropic"

// Anthropic talks to the Anthropic Messages API. Responses are kept as Native records so thinking blocks go
// back with their signatures; see replayable for how they stay valid when the transcript changes.
type Anthropic struct {
	connection
	client anthropic.Client
}

func NewAnthropic(baseURL, apiKey string, calls int, limits map[string]Limits) *Anthropic {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{connection: newConnection(calls, limits), client: anthropic.NewClient(opts...)}
}

func (c *Anthropic) Chat(ctx context.Context, req ChatRequest, onDelta func(Delta)) (ChatResponse, error) {
	if req.Model.Name == "" {
		return ChatResponse{}, fmt.Errorf("llm.Anthropic: model required")
	}
	limits, err := c.Limits(req.Model.Name)
	if err != nil {
		return ChatResponse{}, err
	}
	params, prefix, err := anthropicParams(req, limits.Output)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm.Anthropic: %w", err)
	}
	ctx, release, err := c.acquire(ctx)
	if err != nil {
		return ChatResponse{}, err
	}
	defer release()

	stream := c.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg anthropic.BetaMessage
	started := false
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return anthropicUsage(msg, started), fmt.Errorf("llm.Anthropic: %w", err)
		}
		switch event.Type {
		case "message_start":
			started = true
		case "content_block_delta":
			d := Delta{Content: event.Delta.Text, Reasoning: event.Delta.Thinking}
			if onDelta != nil && (d.Content != "" || d.Reasoning != "") {
				onDelta(d)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return anthropicUsage(msg, started), safeRequestError(err)
	}
	if !started {
		return ChatResponse{}, fmt.Errorf("llm.Anthropic: the stream ended before the response started")
	}
	return anthropicResponse(msg, prefix)
}

func anthropicUsage(msg anthropic.BetaMessage, started bool) ChatResponse {
	u := msg.Usage
	return ChatResponse{
		Usage: Usage{
			PromptTokens:           u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
			CachedPromptTokens:     u.CacheReadInputTokens,
			CacheWritePromptTokens: u.CacheCreationInputTokens,
			CompletionTokens:       u.OutputTokens,
		},
		UsageReported: started,
	}
}

func anthropicResponse(msg anthropic.BetaMessage, prefix string) (ChatResponse, error) {
	resp := anthropicUsage(msg, true)
	switch msg.StopReason {
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
		resp.FinishReason = "stop"
	case anthropic.BetaStopReasonToolUse:
		resp.FinishReason = "tool_calls"
	case anthropic.BetaStopReasonMaxTokens, anthropic.BetaStopReasonModelContextWindowExceeded:
		resp.FinishReason = "length"
	case anthropic.BetaStopReasonRefusal:
		return resp, fmt.Errorf("llm.Anthropic: the model declined to answer")
	case "":
		return resp, fmt.Errorf("llm.Anthropic: the stream ended before the response finished")
	default:
		return resp, fmt.Errorf("llm.Anthropic: unsupported stop reason %q", msg.StopReason)
	}
	var content, reasoning strings.Builder
	var calls []ToolCall
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
		case "tool_use":
			if block.ID == "" || block.Name == "" {
				return resp, fmt.Errorf("llm.Anthropic: tool call without id or name")
			}
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: args})
		}
	}
	raw, err := json.Marshal(msg.ToParam())
	if err != nil {
		return resp, fmt.Errorf("llm.Anthropic: encode the response: %w", err)
	}
	resp.Message = ChatMessage{
		Role: RoleAssistant, Content: content.String(), ReasoningContent: reasoning.String(), ToolCalls: calls,
		Native: native{Provider: providerAnthropic, Message: raw, Prefix: prefix}.encode(),
	}
	return resp, nil
}

// anthropicParams converts the request and returns the hash of its whole prefix, which the response keeps.
func anthropicParams(req ChatRequest, maxTokens int) (anthropic.BetaMessageNewParams, string, error) {
	p := anthropic.BetaMessageNewParams{
		Model: anthropic.Model(req.Model.Name), MaxTokens: int64(maxTokens),
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}
	if req.Model.Thinking == nil || *req.Model.Thinking {
		p.Thinking.OfAdaptive = &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
			// replayable already leaves out the blocks it knows are stale; this keeps any it missed from failing the request.
			BlockBinding: anthropic.BetaThinkingBlockBindingParam{PrefixMismatchBehavior: anthropic.BetaThinkingPrefixMismatchBehaviorDropBlock},
		}
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaThinkingBindingControls2026_08_01}
	} else {
		disabled := anthropic.NewBetaThinkingConfigDisabledParam()
		p.Thinking.OfDisabled = &disabled
	}
	for _, tool := range req.Tools {
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		t := anthropic.BetaToolParam{Name: tool.Name, InputSchema: param.Override[anthropic.BetaToolInputSchemaParam](schema)}
		if tool.Description != "" {
			t.Description = anthropic.String(tool.Description)
		}
		p.Tools = append(p.Tools, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	if len(req.Tools) > 0 {
		switch req.ToolChoice {
		case ToolChoiceAuto:
			p.ToolChoice.OfAuto = &anthropic.BetaToolChoiceAutoParam{DisableParallelToolUse: anthropic.Bool(!req.ParallelToolCalls)}
		case ToolChoiceNone:
			none := anthropic.NewBetaToolChoiceNoneParam()
			p.ToolChoice.OfNone = &none
		default:
			return p, "", fmt.Errorf("unsupported tool choice %q", req.ToolChoice)
		}
	}

	var natives []*native
	for i, m := range req.Messages {
		var blocks []anthropic.BetaContentBlockParamUnion
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				p.System = append(p.System, anthropic.BetaTextBlockParam{Text: m.Content})
			}
			continue
		case RoleUser:
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
			}
			for _, part := range m.Parts {
				switch part.Type {
				case PartImage:
					blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
						Data: base64.StdEncoding.EncodeToString(part.Data), MediaType: anthropic.BetaBase64ImageSourceMediaType(part.MimeType),
					}))
				case PartText:
					blocks = append(blocks, anthropic.NewBetaTextBlock(part.Text))
				default:
					return p, "", fmt.Errorf("message %d: unsupported content part %q", i, part.Type)
				}
			}
		case RoleTool:
			if m.ToolCallID == "" {
				return p, "", fmt.Errorf("message %d: tool message requires tool_call_id", i)
			}
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(m.ToolCallID, m.Content, false))
		case RoleAssistant:
			if n, ok := nativeOf(m, providerAnthropic); ok {
				p.Messages = append(p.Messages, param.Override[anthropic.BetaMessageParam](n.Message))
				natives = append(natives, &n)
				continue
			}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
			}
			for _, call := range m.ToolCalls {
				args := json.RawMessage(call.Arguments)
				if !json.Valid(args) {
					args = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(call.ID, args, call.Name))
			}
		default:
			return p, "", fmt.Errorf("message %d: unsupported role %q", i, m.Role)
		}
		if len(blocks) == 0 {
			continue
		}
		// Tool results and the user messages after them go back as one user turn, as the API expects.
		if n := len(p.Messages); m.Role != RoleAssistant && n > 0 && p.Messages[n-1].Role == anthropic.BetaMessageParamRoleUser {
			p.Messages[n-1].Content = append(p.Messages[n-1].Content, blocks...)
			continue
		}
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == RoleAssistant {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		p.Messages = append(p.Messages, anthropic.BetaMessageParam{Role: role, Content: blocks})
		natives = append(natives, nil)
	}
	prefix, err := replayable(&p, natives)
	return p, prefix, err
}

// replayable keeps the thinking blocks the API will still accept. A thinking block is bound to everything sent
// before it; once that changed (a tool list that grew, a clipped tool result, another system prompt), the block is
// stale. Each response stores the hash of its prefix with thinking left out, so a stale record is one whose hash no
// longer matches. The API accepts removing thinking blocks from the front, so every thinking block up to the last
// stale record is removed and the ones after it are replayed. It returns the hash of the whole request.
func replayable(p *anthropic.BetaMessageNewParams, natives []*native) (string, error) {
	head, err := json.Marshal(struct {
		System []anthropic.BetaTextBlockParam `json:"system"`
		Tools  []anthropic.BetaToolUnionParam `json:"tools"`
	}{p.System, p.Tools})
	if err != nil {
		return "", err
	}
	prefixes := make([]string, len(p.Messages)+1)
	prefixes[0] = chainHash("", head)
	for i, m := range p.Messages {
		raw, err := json.Marshal(m)
		if err != nil {
			return "", fmt.Errorf("message %d: %w", i, err)
		}
		if raw, err = withoutThinking(raw); err != nil {
			return "", fmt.Errorf("message %d: %w", i, err)
		}
		prefixes[i+1] = chainHash(prefixes[i], raw)
	}
	stale := -1
	for i, n := range natives {
		if n != nil && n.Prefix != prefixes[i] {
			stale = i
		}
	}
	for i := 0; i <= stale; i++ {
		if natives[i] == nil {
			continue
		}
		raw, err := withoutThinking(natives[i].Message)
		if err != nil {
			return "", fmt.Errorf("message %d: %w", i, err)
		}
		p.Messages[i] = param.Override[anthropic.BetaMessageParam](json.RawMessage(raw))
	}
	return prefixes[len(p.Messages)], nil
}

func chainHash(prev string, data []byte) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// withoutThinking drops the thinking and redacted_thinking blocks from an encoded message.
func withoutThinking(raw []byte) ([]byte, error) {
	var m struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	kept := m.Content[:0]
	for _, block := range m.Content {
		var b struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return nil, err
		}
		if b.Type != "thinking" && b.Type != "redacted_thinking" {
			kept = append(kept, block)
		}
	}
	m.Content = kept
	return json.Marshal(m)
}

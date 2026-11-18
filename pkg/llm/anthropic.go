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

type Anthropic struct {
	connection
	client anthropic.Client
}

func NewAnthropic(baseURL, apiKey string, calls int, limits map[string]Limits) *Anthropic {
	opts := []option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithEnvironmentProduction(),
		option.WithAPIKey(apiKey), option.WithMaxRetries(retries), option.WithHTTPClient(httpClient)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{connection: newConnection(calls, limits), client: anthropic.NewClient(opts...)}
}

func (c *Anthropic) Chat(ctx context.Context, req ChatRequest, onDelta func(Delta)) (ChatResponse, error) {
	maxTokens, release, err := c.begin(ctx, req)
	if err != nil {
		return ChatResponse{}, err
	}
	defer release()
	params, prefix, err := anthropicParams(req, maxTokens)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm.Anthropic: %w", err)
	}

	stream := c.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg anthropic.BetaMessage
	started := false
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return ChatResponse{}, fmt.Errorf("llm.Anthropic: %w", err)
		}
		switch event.Type {
		case "message_start":
			started = true
		case "content_block_delta":
			d := Delta{Content: event.Delta.Text, Reasoning: event.Delta.Thinking}
			Emit(onDelta, d)
		}
	}
	if err := stream.Err(); err != nil {
		return ChatResponse{}, safeRequestError(err)
	}
	if !started {
		return ChatResponse{}, fmt.Errorf("llm.Anthropic: the stream ended before the response started")
	}
	return anthropicResponse(msg, prefix)
}

func anthropicResponse(msg anthropic.BetaMessage, prefix string) (ChatResponse, error) {
	u := msg.Usage
	resp := ChatResponse{Usage: Usage{
		PromptTokens:           u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CachedPromptTokens:     u.CacheReadInputTokens,
		CacheWritePromptTokens: u.CacheCreationInputTokens,
		CompletionTokens:       u.OutputTokens,
	}}
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

func anthropicParams(req ChatRequest, maxTokens int) (anthropic.BetaMessageNewParams, string, error) {
	p := anthropic.BetaMessageNewParams{
		Model: anthropic.Model(req.Model.Name), MaxTokens: int64(maxTokens),
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}
	if req.Model.thinkingOn() {
		p.Thinking.OfAdaptive = &anthropic.BetaThinkingConfigAdaptiveParam{
			Display:      anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
			BlockBinding: anthropic.BetaThinkingBlockBindingParam{PrefixMismatchBehavior: anthropic.BetaThinkingPrefixMismatchBehaviorDropBlock},
		}
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaThinkingBindingControls2026_08_01}
	} else {
		disabled := anthropic.NewBetaThinkingConfigDisabledParam()
		p.Thinking.OfDisabled = &disabled
	}
	p.Tools, p.ToolChoice = anthropicTools(req)
	natives, err := anthropicMessages(&p, req.Messages)
	if err != nil {
		return p, "", err
	}
	prefix, err := replayable(&p, natives)
	return p, prefix, err
}

func anthropicTools(req ChatRequest) ([]anthropic.BetaToolUnionParam, anthropic.BetaToolChoiceUnionParam) {
	var tools []anthropic.BetaToolUnionParam
	var choice anthropic.BetaToolChoiceUnionParam
	for _, tool := range req.Tools {
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		t := anthropic.BetaToolParam{Name: tool.Name, InputSchema: param.Override[anthropic.BetaToolInputSchemaParam](schema)}
		if tool.Description != "" {
			t.Description = anthropic.String(tool.Description)
		}
		tools = append(tools, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	if len(req.Tools) > 0 {
		choice.OfAuto = &anthropic.BetaToolChoiceAutoParam{}
		if req.ToolChoice == ToolChoiceNone {
			none := anthropic.NewBetaToolChoiceNoneParam()
			choice = anthropic.BetaToolChoiceUnionParam{OfNone: &none}
		}
	}

	return tools, choice
}

func anthropicMessages(p *anthropic.BetaMessageNewParams, messages []ChatMessage) ([]*native, error) {
	var natives []*native
	for i, m := range messages {
		var blocks []anthropic.BetaContentBlockParamUnion
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				p.System = append(p.System, anthropic.BetaTextBlockParam{Text: m.Content})
			}
			continue
		case RoleUser:
			var err error
			blocks, err = userParts(m, anthropic.NewBetaTextBlock, func(part ContentPart) anthropic.BetaContentBlockParamUnion {
				return anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
					Data: base64.StdEncoding.EncodeToString(part.Data), MediaType: anthropic.BetaBase64ImageSourceMediaType(part.MimeType),
				})
			})
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
		case RoleTool:
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
			return nil, fmt.Errorf("message %d: unsupported role %q", i, m.Role)
		}
		if len(blocks) == 0 {
			continue
		}
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
	return natives, nil
}

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

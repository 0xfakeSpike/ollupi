package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

const (
	providerGemini = "gemini"
	// geminiCallPrefix marks a call id ollupi made up because Gemini returned the call without one;
	// such ids are not sent back to Gemini.
	geminiCallPrefix = "gemini-"
)

// Gemini talks to the Gemini API. Responses are kept as Native records so the thought signatures Gemini
// attaches to its parts go back with them.
type Gemini struct {
	connection
	client *genai.Client
}

func NewGemini(baseURL, apiKey string, calls int, limits map[string]Limits) (*Gemini, error) {
	cfg := &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{BaseURL: baseURL}}
	client, err := genai.NewClient(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("llm.Gemini: %w", err)
	}
	return &Gemini{connection: newConnection(calls, limits), client: client}, nil
}

func (c *Gemini) Chat(ctx context.Context, req ChatRequest, onDelta func(Delta)) (ChatResponse, error) {
	if req.Model.Name == "" {
		return ChatResponse{}, fmt.Errorf("llm.Gemini: model required")
	}
	limits, err := c.Limits(req.Model.Name)
	if err != nil {
		return ChatResponse{}, err
	}
	contents, config, err := geminiRequest(req, limits.Output)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm.Gemini: %w", err)
	}
	ctx, release, err := c.acquire(ctx)
	if err != nil {
		return ChatResponse{}, err
	}
	defer release()

	var resp ChatResponse
	var parts []*genai.Part
	var finish genai.FinishReason
	for chunk, err := range c.client.Models.GenerateContentStream(ctx, req.Model.Name, contents, config) {
		if err != nil {
			return ChatResponse{Usage: resp.Usage, UsageReported: resp.UsageReported}, safeRequestError(err)
		}
		if u := chunk.UsageMetadata; u != nil {
			resp.UsageReported = true
			resp.Usage = Usage{
				PromptTokens:       int64(u.PromptTokenCount),
				CachedPromptTokens: int64(u.CachedContentTokenCount),
				CompletionTokens:   int64(u.CandidatesTokenCount + u.ThoughtsTokenCount),
			}
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		candidate := chunk.Candidates[0]
		if candidate.FinishReason != "" {
			finish = candidate.FinishReason
		}
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			// Streamed text arrives in fragments; one part per run of text keeps the stored response small.
			// A signature closes its part, so text after it starts a new one.
			if n := len(parts); n > 0 && isText(parts[n-1]) && isText(part) && parts[n-1].Thought == part.Thought && parts[n-1].ThoughtSignature == nil {
				parts[n-1].Text += part.Text
				parts[n-1].ThoughtSignature = part.ThoughtSignature
			} else {
				parts = append(parts, part)
			}
			d := Delta{Content: part.Text}
			if part.Thought {
				d = Delta{Reasoning: part.Text}
			}
			emit(onDelta, d)
		}
	}
	return geminiResponse(resp, parts, finish)
}

func isText(p *genai.Part) bool {
	return p.FunctionCall == nil && p.InlineData == nil && p.FileData == nil
}

func geminiResponse(resp ChatResponse, parts []*genai.Part, finish genai.FinishReason) (ChatResponse, error) {
	out := ChatResponse{Usage: resp.Usage, UsageReported: resp.UsageReported}
	var content, reasoning strings.Builder
	var calls []ToolCall
	for _, part := range parts {
		switch {
		case part.FunctionCall != nil:
			call := part.FunctionCall
			if call.Name == "" {
				return out, fmt.Errorf("llm.Gemini: tool call without a function name")
			}
			args, err := json.Marshal(call.Args)
			if err != nil || call.Args == nil {
				args = []byte("{}")
			}
			id := call.ID
			if id == "" {
				id = geminiCallPrefix + uuid.NewString()
			}
			calls = append(calls, ToolCall{ID: id, Name: call.Name, Arguments: string(args)})
		case part.Thought:
			reasoning.WriteString(part.Text)
		default:
			content.WriteString(part.Text)
		}
	}
	switch finish {
	case genai.FinishReasonStop:
		out.FinishReason = "stop"
		if len(calls) > 0 {
			out.FinishReason = "tool_calls"
		}
	case genai.FinishReasonMaxTokens:
		out.FinishReason = "length"
	case "":
		return out, fmt.Errorf("llm.Gemini: the stream ended before the response finished")
	default:
		return out, fmt.Errorf("llm.Gemini: the model stopped with %s", finish)
	}
	raw, err := json.Marshal(genai.Content{Role: genai.RoleModel, Parts: parts})
	if err != nil {
		return out, fmt.Errorf("llm.Gemini: encode the response: %w", err)
	}
	out.Message = ChatMessage{
		Role: RoleAssistant, Content: content.String(), ReasoningContent: reasoning.String(), ToolCalls: calls,
		Native: native{Provider: providerGemini, Message: raw}.encode(),
	}
	return out, nil
}

func geminiRequest(req ChatRequest, maxTokens int) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	config := &genai.GenerateContentConfig{MaxOutputTokens: int32(maxTokens)}
	if req.Model.thinkingOn() {
		config.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
	} else {
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](0)}
	}
	if len(req.Tools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, tool := range req.Tools {
			declarations = append(declarations, &genai.FunctionDeclaration{Name: tool.Name, Description: tool.Description, ParametersJsonSchema: tool.Parameters})
		}
		config.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
		switch req.ToolChoice {
		case ToolChoiceAuto:
		case ToolChoiceNone:
			config.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}
		default:
			return nil, nil, fmt.Errorf("unsupported tool choice %q", req.ToolChoice)
		}
	}

	var contents []*genai.Content
	names := map[string]string{}
	add := func(role string, parts []*genai.Part) {
		// Parallel tool results and the user message after them go back as one user turn.
		if n := len(contents); role == genai.RoleUser && n > 0 && contents[n-1].Role == genai.RoleUser {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			return
		}
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	for i, m := range req.Messages {
		var parts []*genai.Part
		switch m.Role {
		case RoleSystem:
			if config.SystemInstruction == nil {
				config.SystemInstruction = &genai.Content{}
			}
			config.SystemInstruction.Parts = append(config.SystemInstruction.Parts, genai.NewPartFromText(m.Content))
			continue
		case RoleUser:
			if m.Content != "" {
				parts = append(parts, genai.NewPartFromText(m.Content))
			}
			for _, part := range m.Parts {
				switch part.Type {
				case PartImage:
					parts = append(parts, genai.NewPartFromBytes(part.Data, part.MimeType))
				case PartText:
					parts = append(parts, genai.NewPartFromText(part.Text))
				default:
					return nil, nil, fmt.Errorf("message %d: unsupported content part %q", i, part.Type)
				}
			}
		case RoleTool:
			name, ok := names[m.ToolCallID]
			if !ok {
				return nil, nil, fmt.Errorf("message %d: tool result %q follows no call", i, m.ToolCallID)
			}
			response := &genai.FunctionResponse{Name: name, Response: map[string]any{"output": m.Content}}
			if !strings.HasPrefix(m.ToolCallID, geminiCallPrefix) {
				response.ID = m.ToolCallID
			}
			parts = append(parts, &genai.Part{FunctionResponse: response})
		case RoleAssistant:
			for _, call := range m.ToolCalls {
				names[call.ID] = call.Name
			}
			if n, ok := nativeOf(m, providerGemini); ok {
				var c genai.Content
				if err := json.Unmarshal(n.Message, &c); err != nil {
					return nil, nil, fmt.Errorf("message %d: decode the native response: %w", i, err)
				}
				contents = append(contents, &c)
				continue
			}
			if m.Content != "" {
				parts = append(parts, genai.NewPartFromText(m.Content))
			}
			for _, call := range m.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					args = map[string]any{}
				}
				fc := &genai.FunctionCall{Name: call.Name, Args: args}
				if !strings.HasPrefix(call.ID, geminiCallPrefix) {
					fc.ID = call.ID
				}
				parts = append(parts, &genai.Part{FunctionCall: fc})
			}
		default:
			return nil, nil, fmt.Errorf("message %d: unsupported role %q", i, m.Role)
		}
		if len(parts) == 0 {
			continue
		}
		role := genai.RoleUser
		if m.Role == RoleAssistant {
			role = genai.RoleModel
		}
		add(role, parts)
	}
	return contents, config, nil
}

package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/workspace"
	"github.com/google/uuid"
)

// contextInput is what the next request is built from: the run's items, the role's system prompt and the offered
// tools, as they are now.
func (rt *Runtime) contextInput() (contextmanager.Input, error) {
	offered, deferred := rt.Role.Offer()
	// The project is named to a run that has the tools that work in it.
	root := ""
	if slices.ContainsFunc(slices.Concat(offered, deferred), func(d tools.ToolDescriptor) bool { return d.Provider == workspace.ProviderName }) {
		root = rt.Role.Workspace().RootPath
	}
	system, err := rt.Role.System(root, deferred)
	if err != nil {
		return contextmanager.Input{}, fmt.Errorf("runtime: %w", err)
	}
	return contextmanager.Input{Items: rt.Role.Conversation(), System: system, Tools: contextmanager.Specs(offered)}, nil
}

func (rt *Runtime) respond(ctx context.Context, choice llm.ToolChoice) (llm.ChatResponse, error) {
	in, err := rt.contextInput()
	if err != nil {
		return llm.ChatResponse{}, err
	}
	prompt, err := rt.Role.Window().Build(in)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("runtime: %w", err)
	}
	if prompt.NeedsCompaction {
		if err := rt.compact(ctx); err != nil {
			return llm.ChatResponse{}, err
		}
		if in, err = rt.contextInput(); err != nil {
			return llm.ChatResponse{}, err
		}
		if prompt, err = rt.Role.Window().Build(in); err != nil {
			return llm.ChatResponse{}, fmt.Errorf("runtime: after compaction: %w", err)
		}
	}
	call := rt.modelCalls() + 1
	r := newResponseRecorder(rt.Role.Turn().ID, prompt.Stats)
	// The text streams into items recorded as it starts and told as it comes; one not recorded stops the rest.
	var streamErr error
	started := time.Now()
	resp, err := rt.send(ctx, prompt, choice, func(d llm.Delta) {
		if streamErr != nil {
			return
		}
		for _, s := range r.delta(d) {
			if s.isNew {
				stored, err := rt.record(*s.item)
				if err != nil {
					streamErr = err
					return
				}
				*s.item = stored
			}
			it := *s.item
			rt.Events.Emit(event.Event{Kind: event.ItemDelta, Thread: rt.Role.Thread(), Turn: rt.Role.Turn(), Item: &it, Delta: s.text})
		}
	})
	r.usage = responseUsage(resp)
	rt.logModelCall(ctx, "model call", started, resp, err)
	if err == nil {
		err = checkResponse(resp, choice)
	}
	if err != nil {
		cause := fmt.Errorf("runtime: model call %d: %w", call, err)
		for _, it := range r.fail(cause) {
			if _, rerr := rt.record(it); rerr != nil {
				return llm.ChatResponse{}, errors.Join(cause, rerr)
			}
		}
		return llm.ChatResponse{}, cause
	}
	if streamErr != nil {
		return llm.ChatResponse{}, streamErr
	}
	for _, it := range r.finish(resp) {
		if _, err := rt.record(it); err != nil {
			return llm.ChatResponse{}, err
		}
	}
	cutOff := resp.FinishReason == "length"
	for _, call := range resp.Message.ToolCalls {
		it := rt.callItem(r.id, call)
		r.carry(&it, resp.Message.Native)
		if cutOff {
			now := time.Now().UTC()
			it.Status, it.Error, it.CompletedAt = db.ItemFailed, cutOffCall, &now
		}
		if _, err := rt.record(it); err != nil {
			return llm.ChatResponse{}, err
		}
	}
	return resp, nil
}

// cutOffCall is why no call of a response the output limit cut off runs: the last may be incomplete, and the model
// would read a missing piece as a failed call instead of a request too long for one response.
const cutOffCall = "not run: the response reached the model's output limit before it was complete; do this in smaller steps, such as a long file written in parts"

func checkResponse(resp llm.ChatResponse, choice llm.ToolChoice) error {
	if len(resp.Message.ToolCalls) > 0 {
		if choice == llm.ToolChoiceNone {
			return fmt.Errorf("model requested tools after tool use was stopped")
		}
		return nil
	}
	if resp.FinishReason == "length" {
		return fmt.Errorf("final answer truncated by the model output limit")
	}
	if strings.TrimSpace(resp.Message.Content) == "" {
		return fmt.Errorf("model returned neither a tool call nor an answer (finish_reason=%s)", resp.FinishReason)
	}
	return nil
}

func (rt *Runtime) compact(ctx context.Context) error {
	in, err := rt.contextInput()
	if err != nil {
		return err
	}
	prompt, err := rt.Role.Window().CompactionPrompt(in)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	it, err := rt.record(db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Role.Turn().ID, Type: db.ItemContextCompaction, Status: db.ItemInProgress, StartedAt: time.Now().UTC(),
		ResponseID: uuid.NewString(), ContextStats: &prompt.Stats,
	})
	if err != nil {
		return err
	}
	started := time.Now()
	resp, err := rt.send(ctx, prompt, llm.ToolChoiceNone, nil)
	it.Usage = responseUsage(resp)
	rt.logModelCall(ctx, "context compaction", started, resp, err)
	summary := strings.TrimSpace(resp.Message.Content)
	switch {
	case err != nil:
	case len(resp.Message.ToolCalls) > 0 || summary == "":
		err = fmt.Errorf("the model returned no summary (finish_reason=%s)", resp.FinishReason)
	case resp.FinishReason == "length":
		err = fmt.Errorf("the summary was truncated by the model output limit")
	}
	if err != nil {
		if _, rerr := rt.complete(withError(it, err.Error()), db.ItemFailed); rerr != nil {
			return rerr
		}
		return fmt.Errorf("runtime: compaction: %w", err)
	}
	it.Text = summary
	if _, err := rt.complete(it, db.ItemCompleted); err != nil {
		return err
	}
	return rt.loadMemory()
}

func (rt *Runtime) send(ctx context.Context, prompt contextmanager.Prompt, choice llm.ToolChoice, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	settings, err := rt.DB.Settings(rt.Role.Thread().UserID)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("runtime: read the redaction setting: %w", err)
	}
	mask := rt.Role.Mask
	if !settings.Redaction {
		mask = func(s string) string { return s }
	}
	messages := make([]llm.ChatMessage, 0, len(prompt.Messages)+1)
	messages = append(messages, llm.ChatMessage{Role: llm.RoleSystem, Content: mask(prompt.System)})
	for _, message := range prompt.Messages {
		message.Content, message.ReasoningContent = mask(message.Content), mask(message.ReasoningContent)
		if message.Parts, err = rt.loadParts(ctx, message.Parts, mask); err != nil {
			return llm.ChatResponse{}, err
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		for i := range message.ToolCalls {
			message.ToolCalls[i].Arguments = mask(message.ToolCalls[i].Arguments)
		}
		messages = append(messages, message)
	}
	content, reasoning := rt.Role.Stream(), rt.Role.Stream()
	restored := func(d llm.Delta) {
		d.Content, d.Reasoning = content.Write(d.Content), reasoning.Write(d.Reasoning)
		if onDelta != nil && (d.Content != "" || d.Reasoning != "") {
			onDelta(d)
		}
	}
	offered, _ := rt.Role.Offer()
	resp, err := rt.LLM.Chat(ctx, llm.ChatRequest{
		Model:    rt.Role.Turn().Model,
		Messages: messages, Tools: contextmanager.Specs(offered), ToolChoice: choice,
		InputTokens: prompt.Stats.CalibratedTokens,
	}, restored)
	if rest := (llm.Delta{Content: content.Flush(), Reasoning: reasoning.Flush()}); onDelta != nil && (rest.Content != "" || rest.Reasoning != "") {
		onDelta(rest)
	}
	resp.Message.Content = rt.Role.Restore(resp.Message.Content)
	resp.Message.ReasoningContent = rt.Role.Restore(resp.Message.ReasoningContent)
	for i := range resp.Message.ToolCalls {
		resp.Message.ToolCalls[i].Arguments = rt.Role.RestoreJSON(resp.Message.ToolCalls[i].Arguments)
	}
	if usage := responseUsage(resp); usage != nil {
		rt.Role.AddUsage(*usage)
	}
	return resp, err
}

func (rt *Runtime) loadParts(ctx context.Context, parts []llm.ContentPart, mask func(string) string) ([]llm.ContentPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := slices.Clone(parts)
	for i, p := range out {
		if rt.Attachments == nil {
			return nil, fmt.Errorf("runtime: the conversation has attachments, but this agent keeps none")
		}
		data, err := rt.Attachments.Get(ctx, p.Path)
		if err != nil {
			return nil, fmt.Errorf("runtime: attachment %s: %w", p.Name, err)
		}
		if p.Type == llm.PartImage {
			out[i].Data = data
		} else {
			out[i].Text = mask("--- file: " + p.Name + " ---\n" + string(data))
		}
	}
	return out, nil
}

// logModelCall records one model request: its model, duration, tokens and how it ended, never its text.
func (rt *Runtime) logModelCall(ctx context.Context, msg string, started time.Time, resp llm.ChatResponse, err error) {
	attrs := []any{"thread", rt.Role.Thread().ID, "turn", rt.Role.Turn().ID, "model", rt.Role.Turn().Model.Name, "ms", time.Since(started).Milliseconds()}
	if err != nil {
		slog.WarnContext(ctx, msg+" failed", append(attrs, "err", err)...)
		return
	}
	slog.InfoContext(ctx, msg, append(attrs, "input", resp.Usage.PromptTokens, "cached", resp.Usage.CachedPromptTokens,
		"output", resp.Usage.CompletionTokens, "finish", resp.FinishReason, "toolCalls", len(resp.Message.ToolCalls))...)
}

func responseUsage(resp llm.ChatResponse) *db.Usage {
	if !resp.UsageReported {
		return nil
	}
	return &db.Usage{
		InputTokens: resp.Usage.PromptTokens, CachedInputTokens: resp.Usage.CachedPromptTokens,
		CacheWriteInputTokens: resp.Usage.CacheWritePromptTokens, OutputTokens: resp.Usage.CompletionTokens,
	}
}

func (rt *Runtime) modelCalls() int {
	seen := map[string]bool{}
	for _, it := range rt.Role.Items() {
		if it.ResponseID != "" && it.Type != db.ItemContextCompaction {
			seen[it.ResponseID] = true
		}
	}
	return len(seen)
}

func (rt *Runtime) callItem(responseID string, call llm.ToolCall) db.ItemRow {
	it := db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Role.Turn().ID, Type: db.ItemToolCall, Status: db.ItemPending,
		ResponseID: responseID, CallID: call.ID, Tool: call.Name, Arguments: call.Arguments, StartedAt: time.Now().UTC(),
	}
	if d, ok := rt.Role.Tool(call.Name); ok {
		it.Server = d.Provider
		annotations := d.Annotations
		it.Annotations = &annotations
	}
	return it
}

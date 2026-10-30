package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (rt *Runtime) contextInput() contextmanager.Input {
	return contextmanager.Input{
		Items:  slices.Concat(rt.History, rt.Items),
		System: rt.system,
		Tools:  Specs(rt.offered),
	}
}

func (rt *Runtime) respond(ctx context.Context, choice llm.ToolChoice) (llm.ChatResponse, error) {
	rt.offered, rt.deferred = Offer(rt.Permissions.Tools(), rt.Window, slices.Concat(rt.History, rt.Items))
	prompt, err := rt.Window.Build(rt.contextInput())
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("runtime: %w", err)
	}
	if prompt.NeedsCompaction {
		if err := rt.compact(ctx); err != nil {
			return llm.ChatResponse{}, err
		}
		if prompt, err = rt.Window.Build(rt.contextInput()); err != nil {
			return llm.ChatResponse{}, fmt.Errorf("runtime: after compaction: %w", err)
		}
	}
	call := rt.modelCalls() + 1
	r := newResponseRecorder(rt, prompt.Stats)
	started := time.Now()
	resp, err := rt.send(ctx, prompt, choice, r.delta)
	r.usage = responseUsage(resp)
	rt.logModelCall("model call", started, resp, err)
	if err == nil {
		err = checkResponse(resp, choice)
	}
	if err != nil {
		return llm.ChatResponse{}, r.fail(fmt.Errorf("runtime: model call %d: %w", call, err))
	}
	if err := r.finish(resp); err != nil {
		return llm.ChatResponse{}, err
	}
	for _, call := range resp.Message.ToolCalls {
		it := rt.callItem(r.id, call)
		r.carry(&it, resp.Message.Native)
		if _, err := rt.record(it); err != nil {
			return llm.ChatResponse{}, err
		}
	}
	return resp, nil
}

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
	prompt, err := rt.Window.CompactionPrompt(rt.contextInput())
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	it, err := rt.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Turn.ID, Type: repo.ItemContextCompaction, Status: repo.ItemInProgress, StartedAt: time.Now().UTC(),
		ResponseID: uuid.NewString(), ContextStats: &prompt.Stats,
	})
	if err != nil {
		return err
	}
	started := time.Now()
	resp, err := rt.send(ctx, prompt, llm.ToolChoiceNone, nil)
	it.Usage = responseUsage(resp)
	rt.logModelCall("context compaction", started, resp, err)
	summary := strings.TrimSpace(resp.Message.Content)
	switch {
	case err != nil:
	case len(resp.Message.ToolCalls) > 0 || summary == "":
		err = fmt.Errorf("the model returned no summary (finish_reason=%s)", resp.FinishReason)
	case resp.FinishReason == "length":
		err = fmt.Errorf("the summary was truncated by the model output limit")
	}
	if err != nil {
		if _, rerr := rt.complete(withError(it, err.Error()), repo.ItemFailed); rerr != nil {
			return rerr
		}
		return fmt.Errorf("runtime: compaction: %w", err)
	}
	it.Text = summary
	if _, err := rt.complete(it, repo.ItemCompleted); err != nil {
		return err
	}
	return rt.loadMemory()
}

func (rt *Runtime) send(ctx context.Context, prompt contextmanager.Prompt, choice llm.ToolChoice, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	settings, err := rt.Settings()
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("runtime: read the redaction setting: %w", err)
	}
	mask := rt.redactor.Mask
	if !settings.Redaction {
		mask = func(s string) string { return s }
	}
	messages := make([]llm.ChatMessage, 0, len(prompt.Messages)+1)
	messages = append(messages, llm.ChatMessage{Role: llm.RoleSystem, Content: mask(prompt.System)})
	for _, message := range prompt.Messages {
		message.Content, message.ReasoningContent = mask(message.Content), mask(message.ReasoningContent)
		if message.Parts, err = rt.loadParts(message.Parts, mask); err != nil {
			return llm.ChatResponse{}, err
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		for i := range message.ToolCalls {
			message.ToolCalls[i].Arguments = mask(message.ToolCalls[i].Arguments)
		}
		messages = append(messages, message)
	}
	content, reasoning := rt.redactor.Stream(), rt.redactor.Stream()
	restored := func(d llm.Delta) {
		d.Content, d.Reasoning = content.Write(d.Content), reasoning.Write(d.Reasoning)
		if onDelta != nil && (d.Content != "" || d.Reasoning != "") {
			onDelta(d)
		}
	}
	resp, err := rt.LLM.Chat(ctx, llm.ChatRequest{
		Model:    rt.Turn.Model,
		Messages: messages, Tools: Specs(rt.offered), ToolChoice: choice,
	}, restored)
	if rest := (llm.Delta{Content: content.Flush(), Reasoning: reasoning.Flush()}); onDelta != nil && (rest.Content != "" || rest.Reasoning != "") {
		onDelta(rest)
	}
	resp.Message.Content = rt.redactor.Restore(resp.Message.Content)
	resp.Message.ReasoningContent = rt.redactor.Restore(resp.Message.ReasoningContent)
	for i := range resp.Message.ToolCalls {
		resp.Message.ToolCalls[i].Arguments = rt.redactor.RestoreJSON(resp.Message.ToolCalls[i].Arguments)
	}
	if usage := responseUsage(resp); usage != nil {
		rt.usage.Add(*usage)
	}
	return resp, err
}

func (rt *Runtime) loadParts(parts []llm.ContentPart, mask func(string) string) ([]llm.ContentPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	if rt.AttachmentsDir == "" {
		return nil, fmt.Errorf("runtime: the conversation has attachments, but this agent keeps none")
	}
	out := slices.Clone(parts)
	for i, p := range out {
		data, err := os.ReadFile(filepath.Join(rt.AttachmentsDir, filepath.FromSlash(p.Path)))
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
func (rt *Runtime) logModelCall(msg string, started time.Time, resp llm.ChatResponse, err error) {
	attrs := []any{"thread", rt.Caller.ThreadID, "turn", rt.Turn.ID, "model", rt.Turn.Model.Name, "ms", time.Since(started).Milliseconds()}
	if err != nil {
		slog.Warn(msg+" failed", append(attrs, "err", err)...)
		return
	}
	slog.Info(msg, append(attrs, "input", resp.Usage.PromptTokens, "cached", resp.Usage.CachedPromptTokens,
		"output", resp.Usage.CompletionTokens, "finish", resp.FinishReason, "toolCalls", len(resp.Message.ToolCalls))...)
}

func responseUsage(resp llm.ChatResponse) *repo.Usage {
	if !resp.UsageReported {
		return nil
	}
	return &repo.Usage{
		InputTokens: resp.Usage.PromptTokens, CachedInputTokens: resp.Usage.CachedPromptTokens,
		CacheWriteInputTokens: resp.Usage.CacheWritePromptTokens, OutputTokens: resp.Usage.CompletionTokens,
	}
}

func (rt *Runtime) modelCalls() int {
	seen := map[string]bool{}
	for _, it := range rt.Items {
		if it.ResponseID != "" && it.Type != repo.ItemContextCompaction {
			seen[it.ResponseID] = true
		}
	}
	return len(seen)
}

func (rt *Runtime) callItem(responseID string, call llm.ToolCall) repo.ItemRow {
	it := repo.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Turn.ID, Type: repo.ItemToolCall, Status: repo.ItemPending,
		ResponseID: responseID, CallID: call.ID, Tool: call.Name, Arguments: call.Arguments, StartedAt: time.Now().UTC(),
	}
	if d, ok := rt.tool(call.Name); ok {
		it.Server = d.Provider
		annotations := d.Annotations
		it.Annotations = &annotations
	}
	return it
}

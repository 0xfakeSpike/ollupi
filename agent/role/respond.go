package role

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

func (rt *Runtime) contextInput() (contextmanager.Input, error) {
	system, err := rt.System()
	if err != nil {
		return contextmanager.Input{}, fmt.Errorf("role: %w", err)
	}
	offered, _ := rt.Offer()
	return contextmanager.Input{Items: rt.conversation(), System: system, Tools: contextmanager.Specs(offered)}, nil
}

func (rt *Runtime) respond(ctx context.Context, choice llm.ToolChoice) error {
	if err := rt.loadMemory(); err != nil {
		return err
	}
	in, err := rt.contextInput()
	if err != nil {
		return err
	}
	prompt, err := rt.window.Build(in)
	if err != nil {
		return fmt.Errorf("role: %w", err)
	}
	if prompt.NeedsCompaction {
		if err := rt.compact(ctx); err != nil {
			return err
		}
		if err := rt.loadMemory(); err != nil {
			return err
		}
		if in, err = rt.contextInput(); err != nil {
			return err
		}
		if prompt, err = rt.window.Build(in); err != nil {
			return fmt.Errorf("role: after compaction: %w", err)
		}
	}
	for _, it := range in.Items {
		prompt.Stats.ThroughSeq = max(prompt.Stats.ThroughSeq, it.Seq)
	}
	r := newResponseRecorder(rt.turn.ID, prompt.Stats)
	var streamErr error
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
			rt.events.Emit(event.Event{Kind: event.ItemDelta, Thread: rt.thread, Turn: rt.turn, Item: &it, Delta: s.text})
		}
	})
	r.usage = responseUsage(resp)
	if err == nil {
		err = checkResponse(resp, choice)
	}
	if err != nil {
		cause := fmt.Errorf("role: model call: %w", err)
		for _, it := range r.fail(cause) {
			if _, rerr := rt.record(it); rerr != nil {
				return errors.Join(cause, rerr)
			}
		}
		return cause
	}
	if streamErr != nil {
		return streamErr
	}
	for _, it := range r.finish(resp) {
		if _, err := rt.record(it); err != nil {
			return err
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
			return err
		}
	}
	return nil
}

// cutOffCall is why no call of a response the output limit cut off runs: the last may be incomplete, and the model
// would read a missing piece as a failed call instead of a request too long for one response.
const cutOffCall = "not run: the response reached the model's output limit before it was complete; do this in smaller steps, such as a long file written in parts"

func checkResponse(resp llm.ChatResponse, choice llm.ToolChoice) error {
	if len(resp.Message.ToolCalls) > 0 {
		if choice == llm.ToolChoiceNone {
			return errors.New("model requested tools after tool use was stopped")
		}
		return nil
	}
	if resp.FinishReason == "length" {
		return errors.New("final answer truncated by the model output limit")
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
	prompt, err := rt.window.CompactionPrompt(in)
	if err != nil {
		return fmt.Errorf("role: %w", err)
	}
	it, err := rt.record(db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemContextCompaction, Status: db.ItemInProgress, StartedAt: time.Now().UTC(),
		ResponseID: uuid.NewString(), ContextStats: &prompt.Stats,
	})
	if err != nil {
		return err
	}
	resp, err := rt.send(ctx, prompt, llm.ToolChoiceNone, nil)
	it.Usage = responseUsage(resp)
	summary := strings.TrimSpace(resp.Message.Content)
	switch {
	case err != nil:
	case len(resp.Message.ToolCalls) > 0 || summary == "":
		err = fmt.Errorf("the model returned no summary (finish_reason=%s)", resp.FinishReason)
	case resp.FinishReason == "length":
		err = errors.New("the summary was truncated by the model output limit")
	}
	if err != nil {
		if _, rerr := rt.complete(withError(it, err.Error()), db.ItemFailed); rerr != nil {
			return rerr
		}
		return fmt.Errorf("role: compaction: %w", err)
	}
	it.Text = summary
	_, err = rt.complete(it, db.ItemCompleted)
	return err
}

func (rt *Runtime) send(ctx context.Context, prompt contextmanager.Prompt, choice llm.ToolChoice, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	var err error
	messages := make([]llm.ChatMessage, 0, len(prompt.Messages)+1)
	messages = append(messages, llm.ChatMessage{Role: llm.RoleSystem, Content: rt.mask(prompt.System)})
	for _, message := range prompt.Messages {
		message.Content, message.ReasoningContent = rt.mask(message.Content), rt.mask(message.ReasoningContent)
		if message.Parts, err = rt.loadParts(ctx, message.Parts); err != nil {
			return llm.ChatResponse{}, err
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		for i := range message.ToolCalls {
			message.ToolCalls[i].Arguments = rt.mask(message.ToolCalls[i].Arguments)
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
	offered, _ := rt.Offer()
	resp, err := rt.client.Chat(ctx, llm.ChatRequest{
		Model:    rt.turn.Model,
		Messages: messages, Tools: contextmanager.Specs(offered), ToolChoice: choice,
		InputTokens: prompt.Stats.CalibratedTokens,
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

func (rt *Runtime) mask(text string) string {
	if !rt.masking {
		return text
	}
	return rt.redactor.Mask(text)
}

func (rt *Runtime) loadParts(ctx context.Context, parts []llm.ContentPart) ([]llm.ContentPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := slices.Clone(parts)
	for i, p := range out {
		if rt.attachments == nil {
			return nil, fmt.Errorf("role: the conversation has attachments, but this agent keeps none")
		}
		data, err := rt.attachments.Get(ctx, p.Path)
		if err != nil {
			return nil, fmt.Errorf("role: attachment %s: %w", p.Name, err)
		}
		if p.Type == llm.PartImage {
			out[i].Data = data
		} else {
			out[i].Text = rt.mask("--- file: " + p.Name + " ---\n" + string(data))
		}
	}
	return out, nil
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

func (rt *Runtime) callItem(responseID string, call llm.ToolCall) db.ItemRow {
	it := db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemToolCall, Status: db.ItemPending,
		ResponseID: responseID, CallID: call.ID, Tool: call.Name, Arguments: call.Arguments, StartedAt: time.Now().UTC(),
	}
	if d, ok := rt.tool(call.Name); ok {
		it.Server = d.Provider
		annotations := d.Annotations
		it.Annotations = &annotations
	}
	return it
}

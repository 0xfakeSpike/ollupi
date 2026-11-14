package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// respond sends the step's request and records the response: its text as it streams, then its calls.
func (tr *turnRun) respond(ctx context.Context, st runtime.CallModel) error {
	call := tr.modelCall()
	response := runtime.NewResponse(tr.state.Turn.ID, st.Prompt.Stats)
	var streamErr error
	onDelta := func(d llm.Delta) {
		if streamErr != nil {
			return
		}
		for _, piece := range response.Delta(d) {
			if piece.New {
				stored, err := tr.record(piece.Item)
				if err != nil {
					streamErr = err
					return
				}
				response.Started(stored)
				piece.Item = stored
			}
			tr.o.emit(Event{Kind: EventItemDelta, Thread: tr.t, Turn: tr.state.Turn, Item: &piece.Item, Delta: piece.Text})
		}
	}
	started := time.Now()
	resp, err := tr.send(ctx, st.Offer, st.Prompt, st.Choice, onDelta)
	tr.logModelCall(ctx, "model call", started, resp, err)
	if err == nil {
		err = runtime.CheckResponse(resp, st.Choice)
	}
	if err != nil {
		cause := fmt.Errorf("orchestrator: model call %d: %w", call, err)
		for _, it := range response.Fail(resp, cause) {
			if _, err := tr.record(it); err != nil {
				return errors.Join(cause, err)
			}
		}
		return cause
	}
	if streamErr != nil {
		return streamErr
	}
	for _, it := range response.Finish(resp, st.Offer) {
		if _, err := tr.record(it); err != nil {
			return err
		}
	}
	return nil
}

// modelCall is the number of the request being made: one more than the responses the turn has.
func (tr *turnRun) modelCall() int {
	seen := map[string]bool{}
	for _, it := range tr.state.Items {
		if it.ResponseID != "" && it.Type != repo.ItemContextCompaction {
			seen[it.ResponseID] = true
		}
	}
	return len(seen) + 1
}

func (tr *turnRun) compact(ctx context.Context, st runtime.Compact) (err error) {
	ctx, span := tracer.Start(ctx, "ollupi.context.compact")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	span.SetAttributes(attribute.Int("ollupi.context.estimated_tokens", st.Prompt.Stats.EstimatedTokens))
	it, err := tr.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: tr.state.Turn.ID, Type: repo.ItemContextCompaction, Status: repo.ItemInProgress, StartedAt: time.Now().UTC(),
		ResponseID: uuid.NewString(), ContextStats: &st.Prompt.Stats,
	})
	if err != nil {
		return err
	}
	started := time.Now()
	resp, err := tr.send(ctx, st.Offer, st.Prompt, llm.ToolChoiceNone, nil)
	tr.logModelCall(ctx, "context compaction", started, resp, err)
	it, cause := runtime.Compacted(it, resp, err)
	if _, err := tr.record(it); err != nil {
		return err
	}
	return cause
}

func (tr *turnRun) send(ctx context.Context, of runtime.Offer, prompt contextmanager.Prompt, choice llm.ToolChoice, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	model := tr.state.Turn.Model
	redactor := redact.New(tr.settings.RedactionKey)
	mask := redactor.Mask
	if !tr.settings.Redaction {
		mask = func(s string) string { return s }
	}
	messages := make([]llm.ChatMessage, 0, len(prompt.Messages)+1)
	messages = append(messages, llm.ChatMessage{Role: llm.RoleSystem, Content: mask(prompt.System)})
	for _, message := range prompt.Messages {
		message.Content, message.ReasoningContent = mask(message.Content), mask(message.ReasoningContent)
		var err error
		if message.Parts, err = tr.loadParts(ctx, message.Parts, mask); err != nil {
			return llm.ChatResponse{}, err
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		for i := range message.ToolCalls {
			message.ToolCalls[i].Arguments = mask(message.ToolCalls[i].Arguments)
		}
		messages = append(messages, message)
	}
	ctx, span := tracer.Start(ctx, "chat "+model.Name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.request.model", model.Name),
		attribute.String("ollupi.connection", model.Connection), attribute.Int("ollupi.llm.estimated_input_tokens", prompt.Stats.CalibratedTokens)))
	defer span.End()
	started, answering := time.Now(), false
	content, reasoning := redactor.Stream(), redactor.Stream()
	restored := func(d llm.Delta) {
		if !answering && (d.Content != "" || d.Reasoning != "") {
			answering = true
			span.SetAttributes(attribute.Int64("ollupi.llm.first_token_ms", time.Since(started).Milliseconds()))
		}
		d.Content, d.Reasoning = content.Write(d.Content), reasoning.Write(d.Reasoning)
		if onDelta != nil && (d.Content != "" || d.Reasoning != "") {
			onDelta(d)
		}
	}
	resp, err := tr.client.Chat(ctx, llm.ChatRequest{
		Model:    model,
		Messages: messages, Tools: role.Specs(of.Offered), ToolChoice: choice,
		InputTokens: prompt.Stats.CalibratedTokens,
	}, restored)
	if rest := (llm.Delta{Content: content.Flush(), Reasoning: reasoning.Flush()}); onDelta != nil && (rest.Content != "" || rest.Reasoning != "") {
		onDelta(rest)
	}
	resp.Message.Content = redactor.Restore(resp.Message.Content)
	resp.Message.ReasoningContent = redactor.Restore(resp.Message.ReasoningContent)
	for i := range resp.Message.ToolCalls {
		resp.Message.ToolCalls[i].Arguments = redactor.RestoreJSON(resp.Message.ToolCalls[i].Arguments)
	}
	if usage := runtime.UsageOf(resp); usage != nil {
		span.SetAttributes(attribute.Int64("gen_ai.usage.input_tokens", usage.InputTokens), attribute.Int64("gen_ai.usage.output_tokens", usage.OutputTokens),
			attribute.Int64("ollupi.llm.cached_input_tokens", usage.CachedInputTokens))
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{resp.FinishReason}),
			attribute.Int("ollupi.llm.tool_calls", len(resp.Message.ToolCalls)))
	}
	return resp, err
}

// loadParts reads the files attached to a message into it.
func (tr *turnRun) loadParts(ctx context.Context, parts []llm.ContentPart, mask func(string) string) ([]llm.ContentPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	if tr.o.cfg.Attachments == nil {
		return nil, errors.New("orchestrator: the conversation has attachments, but this agent keeps none")
	}
	out := slices.Clone(parts)
	for i, p := range out {
		data, err := tr.o.cfg.Attachments.Get(ctx, p.Path)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: attachment %s: %w", p.Name, err)
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
func (tr *turnRun) logModelCall(ctx context.Context, msg string, started time.Time, resp llm.ChatResponse, err error) {
	attrs := []any{"thread", tr.t.ID, "turn", tr.state.Turn.ID, "model", tr.state.Turn.Model.Name, "ms", time.Since(started).Milliseconds()}
	if err != nil {
		slog.WarnContext(ctx, msg+" failed", append(attrs, "err", err)...)
		return
	}
	slog.InfoContext(ctx, msg, append(attrs, "input", resp.Usage.PromptTokens, "cached", resp.Usage.CachedPromptTokens,
		"output", resp.Usage.CompletionTokens, "finish", resp.FinishReason, "toolCalls", len(resp.Message.ToolCalls))...)
}

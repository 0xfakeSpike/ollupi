package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

func (r *Runner) callModel(ctx context.Context, s *step, a agent.CallModel) error {
	if a.Notice != nil {
		return r.append(ctx, s.th.ID, conversation.Entry{Turn: s.turn.ID, Item: uuid.NewString(), Body: *a.Notice})
	}
	recalled, err := r.recall(ctx, s)
	if err != nil {
		return err
	}
	if recalled != nil {
		return r.append(ctx, s.th.ID, conversation.Entry{Turn: s.turn.ID, Item: uuid.NewString(), Body: *recalled})
	}
	in, err := r.input(s)
	if err != nil {
		return err
	}
	req, err := s.window.Build(in)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	req.Stats.Connection = s.turn.Model.Connection
	if req.NeedsCompaction {
		return r.compact(ctx, s, in)
	}
	choice := llm.ToolChoiceAuto
	if a.NoTools {
		choice = llm.ToolChoiceNone
	}
	ans := modelAnswer{response: uuid.NewString(), message: uuid.NewString(), reasoning: uuid.NewString(), sent: time.Now().UTC()}
	resp, err := r.send(ctx, s, req, in.Tools, choice, func(d llm.Delta) {
		ans.hear(d)
		delta := store.Delta{Turn: s.turn.ID, Item: ans.message, Text: d.Content}
		if d.Reasoning != "" {
			delta = store.Delta{Turn: s.turn.ID, Item: ans.reasoning, Reasoning: d.Reasoning}
		}
		r.st.Feed().Publish(store.Event{User: s.th.User, Thread: s.th.ID, Delta: &delta})
	})
	if err == nil {
		err = checkResponse(resp, choice)
	}
	if err != nil {
		cause := fmt.Errorf("run: model call: %w", err)
		if ctx.Err() != nil {
			return cause
		}
		failed := conversation.Entry{Turn: s.turn.ID, Item: uuid.NewString(), Body: conversation.ModelFailed{Error: cause.Error()}}
		return errors.Join(cause, r.append(ctx, s.th.ID, failed))
	}
	entries := ans.entries(s.turn.ID, resp, req.Stats)
	cutOff := resp.FinishReason == "length"
	for _, call := range resp.Message.ToolCalls {
		entries = append(entries, r.decide(ctx, s, ans.response, call, cutOff)...)
	}
	return r.st.Update(ctx, func(tx store.Tx) error {
		v, err := tx.View(s.th.ID)
		if err != nil {
			return err
		}
		heardSince := slices.ContainsFunc(v.ItemsOf(s.turn.ID), func(it conversation.Item) bool {
			return it.Kind == conversation.ItemUserMessage && it.Seq > s.v.Head
		})
		if heardSince {
			return nil
		}
		_, err = tx.Append(s.th.ID, entries...)
		return err
	})
}

type modelAnswer struct {
	response, message, reasoning string
	sent, began, answered        time.Time
}

func (a *modelAnswer) hear(d llm.Delta) {
	firstThought := d.Reasoning != "" && a.began.IsZero()
	firstAnswer := d.Content != "" && !a.began.IsZero() && a.answered.IsZero()
	switch {
	case firstThought:
		a.began = time.Now().UTC()
	case firstAnswer:
		a.answered = time.Now().UTC()
	}
}

func (a *modelAnswer) entries(turn string, resp llm.ChatResponse, stats conversation.ContextStats) []conversation.Entry {
	entries := []conversation.Entry{{Turn: turn, Body: conversation.ModelResponded{Response: a.response, Usage: usage(resp), Stats: stats, Native: resp.Message.Native}}}
	if text := resp.Message.ReasoningContent; text != "" {
		thinking := conversation.Reasoning{Text: text, Response: a.response, Started: cmp.Or(a.began, a.sent), Ended: cmp.Or(a.answered, time.Now().UTC())}
		entries = append(entries, conversation.Entry{Turn: turn, Item: a.reasoning, Body: thinking})
	}
	if text := resp.Message.Content; text != "" {
		entries = append(entries, conversation.Entry{Turn: turn, Item: a.message, Body: conversation.AgentMessage{Text: text, Response: a.response}})
	}
	return entries
}

func checkResponse(resp llm.ChatResponse, choice llm.ToolChoice) error {
	if len(resp.Message.ToolCalls) > 0 {
		if choice == llm.ToolChoiceNone {
			return errors.New("the model called tools after tool use stopped")
		}
		return nil
	}
	if resp.FinishReason == "length" {
		return errors.New("the answer was cut off by the model's output limit")
	}
	if strings.TrimSpace(resp.Message.Content) == "" {
		return fmt.Errorf("the model returned neither a tool call nor an answer (finish_reason=%s)", resp.FinishReason)
	}
	return nil
}

func usage(resp llm.ChatResponse) conversation.Usage {
	u := resp.Usage
	return conversation.Usage{InputTokens: u.PromptTokens, CachedInputTokens: u.CachedPromptTokens,
		CacheWriteInputTokens: u.CacheWritePromptTokens, OutputTokens: u.CompletionTokens}
}

func (r *Runner) recall(ctx context.Context, s *step) (*conversation.Recalled, error) {
	if !s.role.Recalls || !s.settings.Memory {
		return nil, nil
	}
	if recalledSinceCompaction(s.items) {
		return nil, nil
	}
	place := memory.Local(s.th.Workspace)
	var memories []memory.Memory
	err := r.st.Read(ctx, func(rd store.Reader) (err error) {
		memories, err = rd.Memories(s.th.User, memory.GlobalPlace, place)
		return err
	})
	if err != nil {
		return nil, err
	}
	recalled := memory.Recall(memories, place, s.window.MemoryBudget(), latestUserText(s.items))
	if recalled.Text == "" {
		return nil, nil
	}
	return &recalled, nil
}

func latestUserText(items []conversation.Item) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == conversation.ItemUserMessage {
			return items[i].Text
		}
	}
	return ""
}

func recalledSinceCompaction(items []conversation.Item) bool {
	for i := len(items) - 1; i >= 0; i-- {
		switch items[i].Kind {
		case conversation.ItemMemory:
			return true
		case conversation.ItemCompaction:
			return false
		}
	}
	return false
}

func (r *Runner) input(s *step) (prompt.Context, error) {
	skills, err := r.skills(s.role.Name)
	if err != nil {
		return prompt.Context{}, fmt.Errorf("run: the skills of %s: %w", s.role.Name, err)
	}
	system := prompt.System{Role: s.role, Agent: r.name, Prompt: r.prompt, Skills: skills}
	if s.workspace != nil {
		system.Instructions = s.workspace.Instructions
	}
	inProject := slices.ContainsFunc(slices.Concat(s.sent, s.deferred), func(t prompt.Tool) bool { return t.Provider == agent.WorkspaceProvider })
	if inProject && s.workspace != nil {
		system.Project = s.workspace.Root
	}
	return prompt.Context{Items: s.items, Responses: s.v.Responses, System: s.window.System(system, s.deferred), Tools: prompt.Specs(s.sent)}, nil
}

func (r *Runner) compact(ctx context.Context, s *step, in prompt.Context) error {
	req, err := s.window.Compaction(in)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	req.Stats.Connection = s.turn.Model.Connection
	resp, err := r.send(ctx, s, req, in.Tools, llm.ToolChoiceNone, nil)
	if err == nil {
		err = checkResponse(resp, llm.ToolChoiceNone)
	}
	if err != nil {
		return fmt.Errorf("run: compaction: %w", err)
	}
	var through int64
	for _, it := range in.Items {
		through = max(through, it.Updated)
	}
	response := uuid.NewString()
	return r.append(ctx, s.th.ID,
		conversation.Entry{Turn: s.turn.ID, Body: conversation.ModelResponded{Response: response, Usage: usage(resp), Stats: req.Stats, Compaction: true}},
		conversation.Entry{Turn: s.turn.ID, Item: uuid.NewString(), Body: conversation.Compacted{Summary: strings.TrimSpace(resp.Message.Content), Through: through, Response: response, Stats: req.Stats}})
}

func (r *Runner) send(ctx context.Context, s *step, req prompt.Request, tools []llm.ToolSpec, choice llm.ToolChoice, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	client, err := r.models.Client(s.th.User, s.turn.Model)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("the model client of the user: %w", err)
	}
	mask := func(text string) string { return text }
	if s.settings.Redaction {
		mask = s.redactor.Mask
	}
	messages := make([]llm.ChatMessage, 0, len(req.Messages)+1)
	messages = append(messages, llm.ChatMessage{Role: llm.RoleSystem, Content: mask(req.System)})
	for _, m := range req.Messages {
		m.Content, m.ReasoningContent = mask(m.Content), mask(m.ReasoningContent)
		if m.Parts, err = r.parts(ctx, m.Parts, mask); err != nil {
			return llm.ChatResponse{}, err
		}
		m.ToolCalls = slices.Clone(m.ToolCalls)
		for i := range m.ToolCalls {
			m.ToolCalls[i].Arguments = mask(m.ToolCalls[i].Arguments)
		}
		messages = append(messages, m)
	}
	content, reasoning := s.redactor.Stream(), s.redactor.Stream()
	resp, err := client.Chat(ctx, llm.ChatRequest{Model: s.turn.Model, Messages: messages, Tools: tools, ToolChoice: choice,
		InputTokens: req.Stats.CalibratedTokens}, func(d llm.Delta) {
		llm.Emit(onDelta, llm.Delta{Content: content.Write(d.Content), Reasoning: reasoning.Write(d.Reasoning)})
	})
	llm.Emit(onDelta, llm.Delta{Content: content.Flush(), Reasoning: reasoning.Flush()})
	resp.Message.Content = s.redactor.Restore(resp.Message.Content)
	resp.Message.ReasoningContent = s.redactor.Restore(resp.Message.ReasoningContent)
	for i := range resp.Message.ToolCalls {
		resp.Message.ToolCalls[i].Arguments = s.redactor.RestoreJSON(resp.Message.ToolCalls[i].Arguments)
	}
	return resp, err
}

func (r *Runner) parts(ctx context.Context, parts []llm.ContentPart, mask func(string) string) ([]llm.ContentPart, error) {
	out := slices.Clone(parts)
	for i, p := range out {
		data, err := r.attachments.Get(ctx, p.Path)
		if err != nil {
			return nil, fmt.Errorf("run: attachment %s: %w", p.Name, err)
		}
		if p.Type == llm.PartImage {
			out[i].Data = data
		} else {
			out[i].Text = mask("--- file: " + p.Name + " ---\n" + string(data))
		}
	}
	return out, nil
}

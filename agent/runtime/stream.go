package runtime

import (
	"errors"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

type responseRecorder struct {
	rt    *Runtime
	id    string
	stats repo.ContextStats
	usage *repo.Usage

	reasoning     *repo.ItemRow
	agent         *repo.ItemRow
	reasoningText strings.Builder
	agentText     strings.Builder
	err           error
	carried       bool
}

func newResponseRecorder(rt *Runtime, stats repo.ContextStats) *responseRecorder {
	return &responseRecorder{rt: rt, id: uuid.NewString(), stats: stats}
}

func (r *responseRecorder) delta(d llm.Delta) {
	r.add(&r.reasoning, &r.reasoningText, repo.ItemReasoning, d.Reasoning)
	r.add(&r.agent, &r.agentText, repo.ItemAgentMessage, d.Content)
}

func (r *responseRecorder) add(target **repo.ItemRow, text *strings.Builder, kind repo.ItemType, content string) {
	if content == "" || r.err != nil {
		return
	}
	if *target == nil {
		started, err := r.rt.record(repo.ItemRow{
			ID: uuid.NewString(), TurnID: r.rt.Turn.ID, Type: kind, Status: repo.ItemInProgress,
			ResponseID: r.id, StartedAt: time.Now().UTC(),
		})
		if err != nil {
			r.err = err
			return
		}
		*target = &started
	}
	text.WriteString(content)
	r.rt.Delta(**target, content)
}

type responsePart struct {
	kind repo.ItemType
	item *repo.ItemRow
	text string
}

func (r *responseRecorder) finish(resp llm.ChatResponse) error {
	if r.err != nil {
		return r.err
	}
	parts := []responsePart{
		{repo.ItemAgentMessage, r.agent, resp.Message.Content},
		{repo.ItemReasoning, r.reasoning, resp.Message.ReasoningContent},
	}
	for _, part := range parts {
		if part.text == "" && part.item == nil {
			continue
		}
		now := time.Now().UTC()
		it := part.item
		if it == nil {
			it = &repo.ItemRow{ID: uuid.NewString(), TurnID: r.rt.Turn.ID, Type: part.kind, ResponseID: r.id, StartedAt: now}
		}
		it.Text, it.Status, it.CompletedAt = part.text, repo.ItemCompleted, &now
		r.carry(it, resp.Message.Native)
		if _, err := r.rt.record(*it); err != nil {
			return err
		}
	}
	return nil
}

// carry puts the response's context stats, usage and native message on the first of its items recorded:
// the agent message, else the reasoning, else the first tool call, so each response is counted once.
func (r *responseRecorder) carry(it *repo.ItemRow, native []byte) {
	if r.carried {
		return
	}
	r.carried = true
	it.ContextStats, it.Usage, it.Native = &r.stats, r.usage, native
}

func (r *responseRecorder) fail(cause error) error {
	if r.reasoning == nil && r.agent == nil {
		now := time.Now().UTC()
		it := repo.ItemRow{
			ID: uuid.NewString(), TurnID: r.rt.Turn.ID, Type: repo.ItemError, Status: repo.ItemFailed,
			ResponseID: r.id, Text: cause.Error(), Error: cause.Error(), StartedAt: now, CompletedAt: &now,
		}
		r.carry(&it, nil)
		if _, err := r.rt.record(it); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	for _, part := range []responsePart{
		{repo.ItemAgentMessage, r.agent, r.agentText.String()},
		{repo.ItemReasoning, r.reasoning, r.reasoningText.String()},
	} {
		if part.item == nil {
			continue
		}
		now := time.Now().UTC()
		it := part.item
		it.Text, it.Status, it.Error, it.CompletedAt = part.text, repo.ItemFailed, cause.Error(), &now
		r.carry(it, nil)
		if _, err := r.rt.record(*it); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

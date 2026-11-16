package runtime

import (
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

// A responseRecorder keeps what one model response of a turn has made so far: the items its streamed text goes into,
// the text, and its context stats and usage. It records nothing itself: its methods say what the runtime records.
type responseRecorder struct {
	turnID string
	id     string
	stats  db.ContextStats
	usage  *db.Usage

	reasoning     *db.ItemRow
	agent         *db.ItemRow
	reasoningText strings.Builder
	agentText     strings.Builder
	carried       bool
}

func newResponseRecorder(turnID string, stats db.ContextStats) *responseRecorder {
	return &responseRecorder{turnID: turnID, id: uuid.NewString(), stats: stats}
}

// streamed is text the model streamed into item, which is new, to record before the text is told, when the text is
// its first. The runtime puts the item as recorded back where it points.
type streamed struct {
	item  *db.ItemRow
	isNew bool
	text  string
}

// delta takes a part of the response as it streams: the text each of its items takes.
func (r *responseRecorder) delta(d llm.Delta) []streamed {
	var out []streamed
	for _, part := range []struct {
		item    **db.ItemRow
		text    *strings.Builder
		kind    db.ItemType
		content string
	}{
		{&r.reasoning, &r.reasoningText, db.ItemReasoning, d.Reasoning},
		{&r.agent, &r.agentText, db.ItemAgentMessage, d.Content},
	} {
		if part.content == "" {
			continue
		}
		isNew := *part.item == nil
		if isNew {
			*part.item = &db.ItemRow{
				ID: uuid.NewString(), TurnID: r.turnID, Type: part.kind, Status: db.ItemInProgress,
				ResponseID: r.id, StartedAt: time.Now().UTC(),
			}
		}
		part.text.WriteString(part.content)
		out = append(out, streamed{item: *part.item, isNew: isNew, text: part.content})
	}
	return out
}

type responsePart struct {
	kind db.ItemType
	item *db.ItemRow
	text string
}

// finish is the response's agent message and reasoning as it completed, to record.
func (r *responseRecorder) finish(resp llm.ChatResponse) []db.ItemRow {
	var out []db.ItemRow
	for _, part := range []responsePart{
		{db.ItemAgentMessage, r.agent, resp.Message.Content},
		{db.ItemReasoning, r.reasoning, resp.Message.ReasoningContent},
	} {
		if part.text == "" && part.item == nil {
			continue
		}
		now := time.Now().UTC()
		it := db.ItemRow{ID: uuid.NewString(), TurnID: r.turnID, Type: part.kind, ResponseID: r.id, StartedAt: now}
		if part.item != nil {
			it = *part.item
		}
		it.Text, it.Status, it.CompletedAt = part.text, db.ItemCompleted, &now
		r.carry(&it, resp.Message.Native)
		out = append(out, it)
	}
	return out
}

// carry puts the response's context stats, usage and native message on the first of its items recorded:
// the agent message, else the reasoning, else the first tool call, so each response is counted once.
func (r *responseRecorder) carry(it *db.ItemRow, native []byte) {
	if r.carried {
		return
	}
	r.carried = true
	it.ContextStats, it.Usage, it.Native = &r.stats, r.usage, native
}

// fail is the response as it failed for cause, to record: what it streamed, failed, or an error item when it streamed
// nothing.
func (r *responseRecorder) fail(cause error) []db.ItemRow {
	if r.reasoning == nil && r.agent == nil {
		now := time.Now().UTC()
		it := db.ItemRow{
			ID: uuid.NewString(), TurnID: r.turnID, Type: db.ItemError, Status: db.ItemFailed,
			ResponseID: r.id, Text: cause.Error(), Error: cause.Error(), StartedAt: now, CompletedAt: &now,
		}
		r.carry(&it, nil)
		return []db.ItemRow{it}
	}
	var out []db.ItemRow
	for _, part := range []responsePart{
		{db.ItemAgentMessage, r.agent, r.agentText.String()},
		{db.ItemReasoning, r.reasoning, r.reasoningText.String()},
	} {
		if part.item == nil {
			continue
		}
		now := time.Now().UTC()
		it := *part.item
		it.Text, it.Status, it.Error, it.CompletedAt = part.text, db.ItemFailed, cause.Error(), &now
		r.carry(&it, nil)
		out = append(out, it)
	}
	return out
}

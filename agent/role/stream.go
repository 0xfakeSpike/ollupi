package role

import (
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

type responseRecorder struct {
	turnID string
	id     string
	stats  db.ContextStats
	usage  *db.Usage

	reasoning part
	message   part
	carried   bool
}

type part struct {
	kind db.ItemType
	item *db.ItemRow
	text strings.Builder
}

func newResponseRecorder(turnID string, stats db.ContextStats) *responseRecorder {
	return &responseRecorder{turnID: turnID, id: uuid.NewString(), stats: stats,
		reasoning: part{kind: db.ItemReasoning}, message: part{kind: db.ItemAgentMessage}}
}

// The runtime puts the item as recorded back where it points.
type streamed struct {
	item  *db.ItemRow
	isNew bool
	text  string
}

func (r *responseRecorder) delta(d llm.Delta) []streamed {
	var out []streamed
	if d.Reasoning != "" {
		out = append(out, r.stream(&r.reasoning, d.Reasoning))
	}
	if d.Content != "" {
		out = append(out, r.stream(&r.message, d.Content))
	}
	return out
}

func (r *responseRecorder) stream(p *part, text string) streamed {
	isNew := p.item == nil
	if isNew {
		p.item = &db.ItemRow{
			ID: uuid.NewString(), TurnID: r.turnID, Type: p.kind, Status: db.ItemInProgress,
			ResponseID: r.id, StartedAt: time.Now().UTC(),
		}
	}
	p.text.WriteString(text)
	return streamed{item: p.item, isNew: isNew, text: text}
}

// The message first, so that it carries the response's stats.
func (r *responseRecorder) finish(resp llm.ChatResponse) []db.ItemRow {
	var out []db.ItemRow
	if it, ok := r.completed(&r.message, resp.Message.Content, resp.Message.Native); ok {
		out = append(out, it)
	}
	if it, ok := r.completed(&r.reasoning, resp.Message.ReasoningContent, resp.Message.Native); ok {
		out = append(out, it)
	}
	return out
}

func (r *responseRecorder) completed(p *part, text string, native []byte) (db.ItemRow, bool) {
	if text == "" && p.item == nil {
		return db.ItemRow{}, false
	}
	now := time.Now().UTC()
	it := db.ItemRow{ID: uuid.NewString(), TurnID: r.turnID, Type: p.kind, ResponseID: r.id, StartedAt: now}
	if p.item != nil {
		it = *p.item
	}
	it.Text, it.Status, it.CompletedAt = text, db.ItemCompleted, &now
	r.carry(&it, native)
	return it, true
}

// Only the first item recorded carries them, so each response is counted once.
func (r *responseRecorder) carry(it *db.ItemRow, native []byte) {
	if r.carried {
		return
	}
	r.carried = true
	it.ContextStats, it.Usage, it.Native = &r.stats, r.usage, native
}

func (r *responseRecorder) fail(cause error) []db.ItemRow {
	if r.reasoning.item == nil && r.message.item == nil {
		now := time.Now().UTC()
		it := db.ItemRow{
			ID: uuid.NewString(), TurnID: r.turnID, Type: db.ItemError, Status: db.ItemFailed,
			ResponseID: r.id, Text: cause.Error(), Error: cause.Error(), StartedAt: now, CompletedAt: &now,
		}
		r.carry(&it, nil)
		return []db.ItemRow{it}
	}
	var out []db.ItemRow
	if it, ok := r.failed(&r.message, cause); ok {
		out = append(out, it)
	}
	if it, ok := r.failed(&r.reasoning, cause); ok {
		out = append(out, it)
	}
	return out
}

func (r *responseRecorder) failed(p *part, cause error) (db.ItemRow, bool) {
	if p.item == nil {
		return db.ItemRow{}, false
	}
	now := time.Now().UTC()
	it := *p.item
	it.Text, it.Status, it.Error, it.CompletedAt = p.text.String(), db.ItemFailed, cause.Error(), &now
	r.carry(&it, nil)
	return it, true
}

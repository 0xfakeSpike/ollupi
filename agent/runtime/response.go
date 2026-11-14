package runtime

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// A Response gathers what the model sends for one request into the turn's items: its reasoning and its message as
// they stream, then its calls. It does no I/O: whoever runs the request records the items it gives.
type Response struct {
	ID     string
	turnID string
	stats  repo.ContextStats
	usage  *repo.Usage

	reasoning, agent         *repo.ItemRow
	reasoningText, agentText strings.Builder
	carried                  bool
}

func NewResponse(turnID string, stats repo.ContextStats) *Response {
	return &Response{ID: uuid.NewString(), turnID: turnID, stats: stats}
}

// A Piece is text that streamed into an item. New says the piece starts the item, which is recorded before the piece
// is told.
type Piece struct {
	Item repo.ItemRow
	New  bool
	Text string
}

// Delta is the pieces a streamed delta brings: its reasoning, then its message.
func (r *Response) Delta(d llm.Delta) []Piece {
	var pieces []Piece
	for _, part := range []struct {
		target **repo.ItemRow
		text   *strings.Builder
		kind   repo.ItemType
		piece  string
	}{{&r.reasoning, &r.reasoningText, repo.ItemReasoning, d.Reasoning}, {&r.agent, &r.agentText, repo.ItemAgentMessage, d.Content}} {
		if part.piece == "" {
			continue
		}
		started := *part.target == nil
		if started {
			*part.target = &repo.ItemRow{
				ID: uuid.NewString(), TurnID: r.turnID, Type: part.kind, Status: repo.ItemInProgress,
				ResponseID: r.ID, StartedAt: time.Now().UTC(),
			}
		}
		part.text.WriteString(part.piece)
		pieces = append(pieces, Piece{Item: **part.target, New: started, Text: part.piece})
	}
	return pieces
}

// Started keeps the item a new piece started as it was recorded.
func (r *Response) Started(it repo.ItemRow) {
	for _, target := range []**repo.ItemRow{&r.reasoning, &r.agent} {
		if *target != nil && (*target).ID == it.ID {
			*target = &it
		}
	}
}

// Finish is the items that complete the response: its message and its reasoning, then its calls, pending. No call of
// a response that the output limit cut off runs.
func (r *Response) Finish(resp llm.ChatResponse, of Offer) []repo.ItemRow {
	r.usage = UsageOf(resp)
	var out []repo.ItemRow
	for _, part := range []struct {
		kind repo.ItemType
		item *repo.ItemRow
		text string
	}{{repo.ItemAgentMessage, r.agent, resp.Message.Content}, {repo.ItemReasoning, r.reasoning, resp.Message.ReasoningContent}} {
		if part.text == "" && part.item == nil {
			continue
		}
		now := time.Now().UTC()
		it := repo.ItemRow{ID: uuid.NewString(), TurnID: r.turnID, Type: part.kind, ResponseID: r.ID, StartedAt: now}
		if part.item != nil {
			it = *part.item
		}
		it.Text, it.Status, it.CompletedAt = part.text, repo.ItemCompleted, &now
		r.carry(&it, resp.Message.Native)
		out = append(out, it)
	}
	for _, call := range resp.Message.ToolCalls {
		it := repo.ItemRow{
			ID: uuid.NewString(), TurnID: r.turnID, Type: repo.ItemToolCall, Status: repo.ItemPending,
			ResponseID: r.ID, CallID: call.ID, Tool: call.Name, Arguments: call.Arguments, StartedAt: time.Now().UTC(),
		}
		if d, ok := of.Tool(call.Name); ok {
			annotations := d.Annotations
			it.Server, it.Annotations = d.Provider, &annotations
		}
		r.carry(&it, resp.Message.Native)
		if resp.FinishReason == "length" {
			it = completed(withError(it, cutOffCall), repo.ItemFailed)
		}
		out = append(out, it)
	}
	return out
}

// cutOffCall is why no call of a response the output limit cut off runs: the last may be incomplete, and the model
// would read a missing piece as a failed call instead of a request too long for one response.
const cutOffCall = "not run: the response reached the model's output limit before it was complete; do this in smaller steps, such as a long file written in parts"

// Fail is the items that end a response that failed with cause: what streamed of it, failed, or an error in its place.
func (r *Response) Fail(resp llm.ChatResponse, cause error) []repo.ItemRow {
	r.usage = UsageOf(resp)
	if r.reasoning == nil && r.agent == nil {
		now := time.Now().UTC()
		it := repo.ItemRow{
			ID: uuid.NewString(), TurnID: r.turnID, Type: repo.ItemError, Status: repo.ItemFailed,
			ResponseID: r.ID, Text: cause.Error(), Error: cause.Error(), StartedAt: now, CompletedAt: &now,
		}
		r.carry(&it, nil)
		return []repo.ItemRow{it}
	}
	var out []repo.ItemRow
	for _, part := range []struct {
		item *repo.ItemRow
		text string
	}{{r.agent, r.agentText.String()}, {r.reasoning, r.reasoningText.String()}} {
		if part.item == nil {
			continue
		}
		it := *part.item
		it.Text = part.text
		it = completed(withError(it, cause.Error()), repo.ItemFailed)
		r.carry(&it, nil)
		out = append(out, it)
	}
	return out
}

// carry puts the response's context stats, usage and native message on the first of its items: the agent message,
// else the reasoning, else the first tool call, so each response is counted once.
func (r *Response) carry(it *repo.ItemRow, native []byte) {
	if r.carried {
		return
	}
	r.carried = true
	it.ContextStats, it.Usage, it.Native = &r.stats, r.usage, native
}

// CheckResponse says why a response cannot be taken: calls after tool use stopped, or an answer that is cut off or
// empty.
func CheckResponse(resp llm.ChatResponse, choice llm.ToolChoice) error {
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

// UsageOf is what the response says the request used, when it says.
func UsageOf(resp llm.ChatResponse) *repo.Usage {
	if !resp.UsageReported {
		return nil
	}
	return &repo.Usage{
		InputTokens: resp.Usage.PromptTokens, CachedInputTokens: resp.Usage.CachedPromptTokens,
		CacheWriteInputTokens: resp.Usage.CacheWritePromptTokens, OutputTokens: resp.Usage.CompletionTokens,
	}
}

// Ended is the call once its tool returned the result: waiting on what the result awaits, or completed with it, failed
// when it is an error.
func Ended(it repo.ItemRow, result tools.Result) (repo.ItemRow, error) {
	if result.Err == nil && result.Await != nil {
		it.Await = result.Await
		return it, nil
	}
	stored, errText, err := repo.ResultOf(result)
	if err != nil {
		stored, errText, err = repo.ResultOf(tools.Result{Err: fmt.Errorf("tool output cannot be recorded: %w", err)}.For(it.Tool))
		if err != nil {
			return repo.ItemRow{}, err
		}
	}
	it.Result, it.Error = stored, errText
	if errText != "" {
		return completed(it, repo.ItemFailed), nil
	}
	return completed(it, repo.ItemCompleted), nil
}

// Compacted is the compaction item once the model answered the compaction request, and why the run cannot go on when
// it did not summarize.
func Compacted(it repo.ItemRow, resp llm.ChatResponse, err error) (repo.ItemRow, error) {
	it.Usage = UsageOf(resp)
	summary := strings.TrimSpace(resp.Message.Content)
	switch {
	case err != nil:
	case len(resp.Message.ToolCalls) > 0 || summary == "":
		err = fmt.Errorf("the model returned no summary (finish_reason=%s)", resp.FinishReason)
	case resp.FinishReason == "length":
		err = errors.New("the summary was truncated by the model output limit")
	}
	if err != nil {
		return completed(withError(it, err.Error()), repo.ItemFailed), fmt.Errorf("runtime: compaction: %w", err)
	}
	it.Text = summary
	return completed(it, repo.ItemCompleted), nil
}

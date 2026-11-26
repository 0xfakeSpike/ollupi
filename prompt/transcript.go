package prompt

import (
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func (w Window) transcript(obs *observations, natives map[string][]byte, items []conversation.Item, limit int, clipEnded, compacting bool) ([]llm.ChatMessage, int, error) {
	var out []llm.ChatMessage
	latest, current := "", ""
	if !compacting {
		latest = latestResponse(items)
		if len(items) > 0 {
			current = items[len(items)-1].Turn
		}
	}
	if s := latestRecall(items); s != nil && s.Text != "" && !compacting {
		out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: s.Text})
	}
	start := 0
	if c := latestCompaction(items); c >= 0 {
		out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: w.compactedHistory(items[:c], items[c].Text)})
		start = c + 1
	}
	fixed := slices.Clone(out)
	for i := start; i < len(items); {
		it := items[i]
		switch it.Kind {
		case conversation.ItemError, conversation.ItemMemory, conversation.ItemCompaction:
			i++
		case conversation.ItemUserMessage:
			content := sentLine(it) + "\n" + it.Text
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: content, Parts: attachmentParts(it.Attachments)})
			i++
		case conversation.ItemNotice:
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: it.Text})
			i++
		case conversation.ItemReport:
			out = append(out, w.report(obs, items, it))
			i++
		case conversation.ItemReasoning, conversation.ItemAgentMessage, conversation.ItemToolCall:
			j := i
			for j < len(items) && inResponse(items[j], it.Response) {
				j++
			}
			budget := maxObservationTokens
			switch {
			case compacting || it.Response == latest:
				budget = limit
			case clipEnded && it.Turn != current:
				budget = min(limit, endedObservationTokens)
			}
			out = append(out, w.response(obs, natives[it.Response], items[i:j], budget)...)
			i = j
		default:
			return nil, 0, fmt.Errorf("prompt: item %s (%s) has no place in the transcript", it.ID, it.Kind)
		}
	}
	fixedTokens := 0
	if len(fixed) > 0 {
		fixedTokens = llm.EstimateTokens(prettyJSON(fixed))
	}
	return out, fixedTokens, nil
}

func (w Window) report(obs *observations, items []conversation.Item, it conversation.Item) llm.ChatMessage {
	kind, call := "subagent", ""
	launched := func(l conversation.Item) bool { return l.ID == it.Launch }
	if launch := slices.IndexFunc(items, launched); launch >= 0 {
		kind, call = items[launch].Call.Tool, items[launch].Call.Call
	}
	header := fmt.Sprintf("[report of the %s launched by call %s, %q: %s]\n", kind, call, it.Text, it.Status)
	if it.Task != "" {
		header = fmt.Sprintf("[result of the %s call %s, run in the background: %s]\n", kind, call, it.Status)
	}
	return llm.ChatMessage{Role: llm.RoleUser, Content: header + obs.callResult(it, w.observationBudget(1, maxObservationTokens))}
}

func (w Window) response(obs *observations, native []byte, parts []conversation.Item, budget int) []llm.ChatMessage {
	n := 0
	for _, part := range parts {
		if part.Kind == conversation.ItemToolCall {
			n++
		}
	}
	perObservation := w.observationBudget(n, budget)
	assistant := llm.ChatMessage{Role: llm.RoleAssistant, Native: native}
	var calls []llm.ChatMessage
	for _, part := range parts {
		switch part.Kind {
		case conversation.ItemReasoning:
			assistant.ReasoningContent += part.Text
		case conversation.ItemAgentMessage:
			assistant.Content += part.Text
		case conversation.ItemToolCall:
			assistant.ToolCalls = append(assistant.ToolCalls, llm.ToolCall{ID: part.Call.Call, Name: part.Call.Tool, Arguments: part.Call.Arguments})
			calls = append(calls, llm.ChatMessage{Role: llm.RoleTool, ToolCallID: part.Call.Call, Content: obs.callResult(part, perObservation)})
		}
	}
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		return nil
	}
	return append([]llm.ChatMessage{assistant}, calls...)
}

func inResponse(it conversation.Item, response string) bool {
	switch it.Kind {
	case conversation.ItemReasoning, conversation.ItemAgentMessage, conversation.ItemToolCall:
		return it.Response == response
	}
	return false
}

func (w Window) observationBudget(calls, limit int) int {
	return min(limit, max(minPooledTokens, min(w.inputTokens/8, percentOf(w.inputTokens, responsePercent)/max(1, calls))))
}

// sentLine heads a user message with the local time it was sent and, for one a hub conversation sent, who sent it.
func sentLine(it conversation.Item) string {
	return "[sent " + it.StartedAt.In(time.Local).Format(sentLayout) + onBehalf(it.From) + "]"
}

func onBehalf(from *conversation.Sender) string {
	if from == nil {
		return ""
	}
	return fmt.Sprintf(" by the hub conversation %q on the user's behalf", from.Title)
}

func attachmentParts(atts []conversation.Attachment) []llm.ContentPart {
	var parts []llm.ContentPart
	for _, att := range atts {
		kind := llm.PartText
		if att.Kind == "image" {
			kind = llm.PartImage
		}
		parts = append(parts, llm.ContentPart{Type: kind, Name: att.Name, MimeType: att.MimeType, Tokens: att.Tokens, Path: att.File})
	}
	return parts
}

func latestResponse(items []conversation.Item) string {
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; inResponse(it, it.Response) && it.Response != "" {
			return it.Response
		}
	}
	return ""
}

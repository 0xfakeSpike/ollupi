package prompt

import (
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// fixed is how many of the tokens are the recalled memories and the compacted history, which compaction cannot shrink.
func (w Window) transcript(obs *observations, natives map[string][]byte, items []conversation.Item, limit int, compacting bool) ([]llm.ChatMessage, int, error) {
	var out []llm.ChatMessage
	latest := ""
	if !compacting {
		latest = latestResponse(items)
	}
	if s := snapshot(items); s != nil && s.Text != "" && !compacting {
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
			content := "[sent " + it.StartedAt.In(time.Local).Format(sentLayout) + "]\n" + it.Text
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: content, Parts: attachmentParts(it.Attachments)})
			i++
		case conversation.ItemNotice:
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: it.Text})
			i++
		case conversation.ItemReport:
			// A subagent's report is data too: the subagent's words.
			content := obs.callResult(it, w.observationBudget(1, maxObservationTokens))
			kind, call := "subagent", ""
			if launch := slices.IndexFunc(items, func(l conversation.Item) bool { return l.ID == it.Launch }); launch >= 0 {
				kind, call = items[launch].Call.Tool, items[launch].Call.Call
			}
			header := fmt.Sprintf("[report of the %s launched by call %s, %q: %s]\n", kind, call, it.Text, it.Status)
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: header + content})
			i++
		case conversation.ItemReasoning, conversation.ItemAgentMessage, conversation.ItemToolCall:
			response := it.Response
			n := 0
			j := i
			for ; j < len(items) && inResponse(items[j], response); j++ {
				if items[j].Kind == conversation.ItemToolCall {
					n++
				}
			}
			budget := maxObservationTokens
			if compacting || response == latest {
				budget = limit
			}
			perObservation := w.observationBudget(n, budget)
			assistant := llm.ChatMessage{Role: llm.RoleAssistant, Native: natives[response]}
			var calls []llm.ChatMessage
			for _, part := range items[i:j] {
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
			if assistant.Content != "" || len(assistant.ToolCalls) > 0 {
				out = append(out, assistant)
				out = append(out, calls...)
			}
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

func inResponse(it conversation.Item, response string) bool {
	switch it.Kind {
	case conversation.ItemReasoning, conversation.ItemAgentMessage, conversation.ItemToolCall:
		return it.Response == response
	}
	return false
}

func (w Window) observationBudget(calls, limit int) int {
	return min(limit, max(minPooledTokens, min(w.inputTokens/8, share(w.inputTokens, responsePercent)/max(1, calls))))
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

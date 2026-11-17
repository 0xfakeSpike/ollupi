package contextmanager

import (
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (m *Manager) transcript(obs *observations, items []db.ItemRow, limit int, compacting bool) ([]llm.ChatMessage, int, error) {
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
		out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: m.compactedHistory(items[:c], items[c].Text)})
		start = c + 1
	}
	fixed := slices.Clone(out)
	for i := start; i < len(items); {
		it := items[i]
		switch {
		case it.Type == db.ItemError && it.ContextStats != nil:

			i++
		case it.Type == db.ItemUserMessage || it.Type == db.ItemError:
			content := it.Text
			if it.Type == db.ItemUserMessage {
				content = "[sent " + it.StartedAt.In(time.Local).Format(sentLayout) + "]\n" + content
			}
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: content, Parts: attachmentParts(it.Attachments)})
			i++
		case it.Type == db.ItemAgentReport:
			// A subagent's report is data too: the subagent's words.
			content, err := obs.callResult(&items[i], m.observationBudget(1, maxObservationTokens))
			if err != nil {
				return nil, 0, err
			}
			header := fmt.Sprintf("[report of the %s launched by call %s, %q: %s]\n", it.Tool, it.CallID, it.Text, it.Status)
			out = append(out, llm.ChatMessage{Role: llm.RoleUser, Content: header + content})
			i++
		case it.Type == db.ItemMemoryContext || it.Type == db.ItemContextCompaction:

			i++
		case it.ResponseID != "":
			assistant := llm.ChatMessage{Role: llm.RoleAssistant}
			var calls []llm.ChatMessage
			n := 0
			for k := i; k < len(items) && items[k].ResponseID == it.ResponseID; k++ {
				if items[k].IsCall() {
					n++
				}
			}
			budget := maxObservationTokens
			if compacting || it.ResponseID == latest {
				budget = limit
			}
			perObservation := m.observationBudget(n, budget)
			j := i
			for ; j < len(items) && items[j].ResponseID == it.ResponseID; j++ {
				part := items[j]
				if part.Native != nil {
					assistant.Native = part.Native
				}
				switch {
				case part.Type == db.ItemReasoning || part.Type == db.ItemAgentMessage:

					if part.Status != db.ItemCompleted {
						continue
					}
					if part.Type == db.ItemReasoning {
						assistant.ReasoningContent += part.Text
					} else {
						assistant.Content += part.Text
					}
				case part.IsCall():
					assistant.ToolCalls = append(assistant.ToolCalls, llm.ToolCall{ID: part.CallID, Name: part.Tool, Arguments: part.Arguments})
					content, err := obs.callResult(&items[j], perObservation)
					if err != nil {
						return nil, 0, err
					}
					calls = append(calls, llm.ChatMessage{Role: llm.RoleTool, ToolCallID: part.CallID, Content: content})
				default:
					return nil, 0, fmt.Errorf("context: item %s (%s) cannot be part of a model response", part.ID, part.Type)
				}
			}

			if assistant.Content != "" || len(assistant.ToolCalls) > 0 {
				out = append(out, assistant)
				out = append(out, calls...)
			}
			i = j
		default:
			return nil, 0, fmt.Errorf("context: item %s (%s) has no place in the transcript", it.ID, it.Type)
		}
	}
	fixedTokens := 0
	if len(fixed) > 0 {
		fixedTokens = llm.EstimateTokens(prettyJSON(fixed))
	}
	return out, fixedTokens, nil
}

func (m *Manager) observationBudget(calls, limit int) int {
	return min(limit, max(minPooledTokens, min(m.inputTokens/8, share(m.inputTokens, responsePercent)/max(1, calls))))
}

func attachmentParts(atts []db.Attachment) []llm.ContentPart {
	var parts []llm.ContentPart
	for _, att := range atts {
		kind := llm.PartText
		if att.Kind == db.AttachmentImage {
			kind = llm.PartImage
		}
		parts = append(parts, llm.ContentPart{Type: kind, Name: att.Name, MimeType: att.MimeType, Tokens: att.Tokens, Path: att.File})
	}
	return parts
}

func latestResponse(items []db.ItemRow) string {
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.ResponseID != "" && (it.IsCall() || it.Type == db.ItemReasoning || it.Type == db.ItemAgentMessage) {
			return it.ResponseID
		}
	}
	return ""
}

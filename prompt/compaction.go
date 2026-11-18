package prompt

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

const compactionInstructions = `You are performing a CONTEXT CHECKPOINT COMPACTION. Write a handoff summary for another language model that will resume this conversation. Include:
- the user's requests, preferences, and constraints, and the decisions made;
- current progress: what has been done, with the exact facts, ids, paths, and results it produced;
- what remains to be done, as clear next steps.
Be concise and structured. Write in the user's language. Do not call tools.`

func (w Window) Compaction(in Input) (Request, error) {
	instructions := llm.ChatMessage{Role: llm.RoleUser, Content: compactionInstructions}
	keep := 0
	if latestCompaction(in.Items) >= 0 {
		keep = 1
	}
	obs := newObservations(in.Items, w.artifactBudget())
	natives := nativesOf(in.Responses)
	rendered := map[int][]llm.ChatMessage{}
	render := func(limit int) ([]llm.ChatMessage, error) {
		if messages, ok := rendered[limit]; ok {
			return messages, nil
		}
		messages, _, err := w.transcript(obs, natives, in.Items, limit, true)
		rendered[limit] = messages
		return messages, err
	}
	full, err := render(maxObservationTokens)
	if err != nil {
		return Request{}, err
	}
	for from := keep; ; {
		r, err := w.fitted(func(limit int) (Request, error) {
			messages, err := render(limit)
			if err != nil {
				return Request{}, err
			}
			r := Request{System: in.System, Messages: slices.Concat(messages[:keep], messages[from:], []llm.ChatMessage{instructions})}
			r.Stats = w.stats(in.System, r.Messages, in.Tools)
			w.calibrate(&r.Stats, in.Items, in.Responses)
			return r, nil
		})
		if err != nil {
			return Request{}, err
		}
		if r.Stats.CalibratedTokens <= w.inputTokens {
			return r, nil
		}
		if from = nextUserMessage(full, from); from < 0 {
			return Request{}, fmt.Errorf("prompt: the compaction request needs %d tokens even with every tool result clipped; model %s allows %d", r.Stats.CalibratedTokens, w.model, w.inputTokens)
		}
	}
}

func latestCompaction(items []conversation.Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == conversation.ItemCompaction {
			return i
		}
	}
	return -1
}

func hasItemsAfterCompaction(items []conversation.Item) bool {
	for _, it := range items[latestCompaction(items)+1:] {
		if it.Kind != conversation.ItemCompaction && it.Kind != conversation.ItemMemory {
			return true
		}
	}
	return false
}

const summaryPrefix = "Another language model started this conversation and produced the summary below. Use it to continue the work without repeating what was already done."

func (w Window) compactedHistory(before []conversation.Item, summary string) string {
	var b strings.Builder
	for i := len(before) - 1; i >= 0; i-- {
		if it := before[i]; it.Kind == conversation.ItemUserMessage {
			b.WriteString("Latest user message before this summary: [sent " + it.StartedAt.In(time.Local).Format(sentLayout) + "]\n\n")
			break
		}
	}
	if users := recentUserMessages(before, share(w.inputTokens, userMessagePercent)); len(users) > 0 {
		b.WriteString("Earlier messages from the user:\n")
		for _, text := range users {
			b.WriteString("- " + strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n  ") + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(summaryPrefix + "\n\n" + strings.TrimSpace(summary))
	kept, dropped := recentArtifacts(activeArtifacts(before), w.artifactBudget())
	for _, artifact := range kept {
		fmt.Fprintf(&b, "\n\n--- active %s: %s ---\n%s", artifact.Kind, artifact.Name, artifact.Content)
	}
	for _, artifact := range dropped {
		fmt.Fprintf(&b, "\n\n--- %s %s was active earlier; its content no longer fits here, so load it again if it is still needed ---", artifact.Kind, artifact.Name)
	}
	return b.String()
}

func recentUserMessages(items []conversation.Item, budget int) []string {
	var out []string
	used := 0
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != conversation.ItemUserMessage {
			continue
		}
		cost := llm.EstimateTokens(items[i].Text) + 4
		if used+cost > budget {
			break
		}
		used += cost
		out = append([]string{items[i].Text}, out...)
	}
	return out
}

func nextUserMessage(messages []llm.ChatMessage, from int) int {
	for i := from + 1; i < len(messages); i++ {
		if messages[i].Role == llm.RoleUser {
			return i
		}
	}
	return -1
}

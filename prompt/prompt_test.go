package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

type log struct {
	t *testing.T
	v conversation.View
}

func (l *log) add(turn, item string, b conversation.Body) {
	l.t.Helper()
	e := conversation.Entry{Seq: l.v.Head + 1, Turn: turn, Item: item, At: time.Unix(1_700_000_000+l.v.Head, 0), Body: b}
	if err := conversation.Check(l.v, e); err != nil {
		l.t.Fatal(err)
	}
	l.v.Apply(e)
}

var wide = NewWindow("m", llm.Limits{Context: 200_000, Output: 8_000})

func build(t *testing.T, w Window, v conversation.View) Request {
	t.Helper()
	r, err := w.Build(Context{Items: Transcript(v), Responses: v.Responses, System: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func roles(messages []llm.ChatMessage) string {
	var out []string
	for _, m := range messages {
		out = append(out, string(m.Role))
	}
	return strings.Join(out, ",")
}

func TestATurnReadsAsMessagesCallsAndResults(t *testing.T) {
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{})
	l.add("t", "m", conversation.UserMessage{Text: "list"})
	l.add("t", "", conversation.ModelResponded{Response: "r1", Native: []byte("native")})
	l.add("t", "c", conversation.ToolCall{Call: "call_1", Tool: "run_command", Arguments: `{"command":"ls"}`, Response: "r1"})
	l.add("t", "c", conversation.ToolStarted{})
	l.add("t", "c", conversation.ToolResult{Output: json.RawMessage(`"a.go"`)})
	l.add("t", "", conversation.ModelResponded{Response: "r2"})
	l.add("t", "a", conversation.AgentMessage{Text: "a.go", Response: "r2"})
	r := build(t, wide, l.v)
	if got := roles(r.Messages); got != "user,assistant,tool,assistant" {
		t.Fatalf("roles %s", got)
	}
	if !strings.HasPrefix(r.Messages[0].Content, "[sent ") || !strings.HasSuffix(r.Messages[0].Content, "\nlist") {
		t.Errorf("the message: %q", r.Messages[0].Content)
	}
	if call := r.Messages[1]; len(call.ToolCalls) != 1 || call.ToolCalls[0].ID != "call_1" || string(call.Native) != "native" {
		t.Errorf("the call: %+v", call)
	}
	if r.Messages[2].Content != "a.go" || r.Messages[2].ToolCallID != "call_1" || r.Messages[3].Content != "a.go" {
		t.Errorf("the result and the answer: %+v", r.Messages[2:])
	}
	if r.NeedsCompaction || r.Stats.EstimatedTokens == 0 || r.Stats.Model != "m" {
		t.Errorf("stats %+v", r.Stats)
	}
}

func TestAMessageTheHubSentNamesTheHubConversation(t *testing.T) {
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{})
	l.add("t", "m", conversation.UserMessage{Text: "write the release notes", From: &conversation.Sender{Thread: "h", Title: "plan"}})
	l.add("t", "s", conversation.UserMessage{Text: "and the changelog"})
	r := build(t, wide, l.v)
	hub, user := r.Messages[0].Content, r.Messages[1].Content
	if !strings.HasPrefix(hub, "[sent ") || !strings.HasSuffix(hub, ` by the hub conversation "plan" on the user's behalf]`+"\nwrite the release notes") {
		t.Errorf("the hub's message: %q", hub)
	}
	if strings.Contains(user, "hub") {
		t.Errorf("the user's message: %q", user)
	}
}

func TestATurnFromBehindReadsAfterTheOneBeforeIt(t *testing.T) {
	l := &log{t: t}
	l.add("t1", "", conversation.TurnQueued{})
	l.add("t1", "m1", conversation.UserMessage{Text: "first"})
	l.add("t2", "", conversation.TurnQueued{})
	l.add("t2", "m2", conversation.UserMessage{Text: "second"})
	l.add("t1", "", conversation.ModelResponded{Response: "r1"})
	l.add("t1", "a1", conversation.AgentMessage{Text: "one", Response: "r1"})
	if items := Transcript(l.v); len(items) != 2 {
		t.Fatalf("the turn behind is shown: %d items", len(items))
	}
	l.add("t1", "", conversation.TurnEnded{Status: conversation.Completed})
	r := build(t, wide, l.v)
	if len(r.Messages) != 3 || !strings.HasSuffix(r.Messages[2].Content, "second") {
		t.Errorf("messages %+v", r.Messages)
	}
}

func TestALongOutputIsClippedWithHowToReadTheRest(t *testing.T) {
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{})
	l.add("t", "", conversation.ModelResponded{Response: "r1"})
	l.add("t", "c", conversation.ToolCall{Call: "call_1", Tool: "read_file", Response: "r1"})
	l.add("t", "c", conversation.ToolStarted{})
	long, _ := json.Marshal(strings.Repeat("line of output\n", 20_000))
	l.add("t", "c", conversation.ToolResult{Output: long})
	r := build(t, wide, l.v)
	result := r.Messages[1].Content
	if !strings.Contains(result, agent.ReadOutputTool) || !strings.Contains(result, `output_id "c"`) || llm.EstimateTokens(result) > maxObservationTokens+100 {
		t.Errorf("the clipped output: %d tokens, %q", llm.EstimateTokens(result), result[:200])
	}
}

func TestAnEndedTurnKeepsLessOfItsOutputs(t *testing.T) {
	l := &log{t: t}
	long, _ := json.Marshal(strings.Repeat("line of output\n", 20_000))
	for _, turn := range []string{"t1", "t2"} {
		l.add(turn, "", conversation.TurnQueued{})
		l.add(turn, "m"+turn, conversation.UserMessage{Text: "read it"})
		l.add(turn, "", conversation.ModelResponded{Response: "r1" + turn})
		l.add(turn, "c"+turn, conversation.ToolCall{Call: "call_" + turn, Tool: "read_file", Response: "r1" + turn})
		l.add(turn, "c"+turn, conversation.ToolStarted{})
		l.add(turn, "c"+turn, conversation.ToolResult{Output: long})
		l.add(turn, "", conversation.ModelResponded{Response: "r2" + turn})
		l.add(turn, "a"+turn, conversation.AgentMessage{Text: "read", Response: "r2" + turn})
		if turn == "t1" {
			l.add(turn, "", conversation.TurnEnded{Status: conversation.Completed})
		}
	}
	r := build(t, wide, l.v)
	var ended, current string
	for _, m := range r.Messages {
		switch m.ToolCallID {
		case "call_t1":
			ended = m.Content
		case "call_t2":
			current = m.Content
		}
	}
	if !strings.Contains(ended, `output_id "ct1"`) || llm.EstimateTokens(ended) > endedObservationTokens+100 {
		t.Errorf("the ended turn's output: %d tokens", llm.EstimateTokens(ended))
	}
	if llm.EstimateTokens(current) <= endedObservationTokens+100 {
		t.Errorf("the current turn's output: %d tokens", llm.EstimateTokens(current))
	}
}

func TestAnOutputKeepsItsSeparatorLines(t *testing.T) {
	file := "---\ntitle: a\n---\n| a | b |\n|---|---|\n| 1 | 2 |\n***"
	raw, _ := json.Marshal(map[string]any{"content": file})
	if got := OutputText(raw); !strings.Contains(got, `"---\ntitle: a\n---\n| a | b |\n|---|---|\n| 1 | 2 |\n***"`) {
		t.Errorf("the output: %s", got)
	}
}

func TestALongHistoryAsksForCompaction(t *testing.T) {
	w := NewWindow("m", llm.Limits{Context: 6_000, Output: 1_000})
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{})
	for i := range 12 {
		l.add("t", fmt.Sprint("m", i), conversation.UserMessage{Text: strings.Repeat("words of the user ", 80)})
		resp := fmt.Sprint("r", i)
		l.add("t", "", conversation.ModelResponded{Response: resp})
		l.add("t", fmt.Sprint("a", i), conversation.AgentMessage{Text: strings.Repeat("words of the agent ", 80), Response: resp})
	}
	in := Context{Items: Transcript(l.v), Responses: l.v.Responses}
	r, err := w.Build(in)
	if err != nil || !r.NeedsCompaction {
		t.Fatalf("a history over the window: needs compaction %v, %v", r.NeedsCompaction, err)
	}
	c, err := w.Compaction(in)
	if err != nil || c.Stats.CalibratedTokens > w.inputTokens || !strings.Contains(c.Messages[len(c.Messages)-1].Content, "CONTEXT CHECKPOINT COMPACTION") {
		t.Fatalf("the compaction request: %d tokens, %v", c.Stats.CalibratedTokens, err)
	}
	l.add("t", "", conversation.ModelResponded{Response: "rc", Compaction: true})
	l.add("t", "s", conversation.Compacted{Summary: "the gist", Through: l.v.Head, Response: "rc"})
	l.add("t", "m-last", conversation.UserMessage{Text: "go on"})
	r = build(t, w, l.v)
	if r.NeedsCompaction || len(r.Messages) != 2 || !strings.Contains(r.Messages[0].Content, "the gist") {
		t.Errorf("after compaction: %d messages, needs %v", len(r.Messages), r.NeedsCompaction)
	}
}

func TestToolsThatDoNotFitAreNamedAndLoadedOnes(t *testing.T) {
	w := NewWindow("m", llm.Limits{Context: 20_000, Output: 2_000})
	schema := map[string]any{"type": "object", "description": strings.Repeat("a long schema ", 40)}
	var visible []Tool
	visible = append(visible, Tool{Name: agent.LoadToolsTool, Provider: "tools", Schema: schema}, Tool{Name: "read_file", Provider: agent.WorkspaceProvider, Schema: schema})
	for i := range 30 {
		visible = append(visible, Tool{Name: fmt.Sprint("github__tool", i), Provider: "github", Schema: schema})
	}
	sent, deferred := w.Offer(visible, nil, nil)
	if len(sent) != 2 || len(deferred) != 30 {
		t.Fatalf("sent %d, deferred %d", len(sent), len(deferred))
	}
	load := []conversation.Item{{Kind: conversation.ItemToolCall, Status: conversation.Done,
		Call: &conversation.ToolCall{Tool: agent.LoadToolsTool}, Result: &conversation.Result{Output: json.RawMessage(`{"loaded":["github__tool3"]}`)}}}
	if sent, _ := w.Offer(visible, nil, load); len(sent) != 3 || sent[2].Name != "github__tool3" {
		t.Errorf("after loading: %d sent", len(sent))
	}
	system := w.System(System{Role: mustRole(agent.FullMain), Agent: "Ollupi", Project: "/p"}, deferred)
	for _, want := range []string{"You are Ollupi", "# DEFERRED TOOLS", "- github: github__tool0,", "# PROJECT\nThis conversation works in the project at /p"} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
}

func mustRole(name agent.Name) agent.Role {
	r, _ := agent.RoleOf(name)
	return r
}

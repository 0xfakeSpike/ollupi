package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAnthropic answers every request with the next scripted stream and keeps the request bodies.
type fakeAnthropic struct {
	mu      sync.Mutex
	streams [][]string
	bodies  []map[string]any
	headers []http.Header
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.bodies, f.headers = append(f.bodies, body), append(f.headers, r.Header.Clone())
	events := f.streams[0]
	f.streams = f.streams[1:]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &head)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, e)
	}
}

func anthropicTurn(signature string, calls ...string) []string {
	events := []string{
		`{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look it up"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	stop := "end_turn"
	if len(calls) == 0 {
		events = append(events,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":1}`)
	}
	for i, id := range calls {
		events = append(events,
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":"lookup","input":{}}}`, i+1, id),
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"%s\"}"}}`, i+1, id),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1))
		stop = "tool_use"
	}
	return append(events,
		`{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`)
}

var lookup = ToolSpec{Name: "lookup", Description: "Look something up.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}

// blocks returns the content block types of each message of a recorded request, with each thinking block's signature.
func blocks(body map[string]any) [][]string {
	var out [][]string
	for _, m := range body["messages"].([]any) {
		var types []string
		for _, b := range m.(map[string]any)["content"].([]any) {
			block := b.(map[string]any)
			t := block["type"].(string)
			if t == "thinking" {
				t += ":" + block["signature"].(string)
			}
			types = append(types, t)
		}
		out = append(out, types)
	}
	return out
}

func TestAnthropicReplaysThinkingUntilThePrefixChanges(t *testing.T) {
	fake := &fakeAnthropic{streams: [][]string{anthropicTurn("sigA", "call1", "call2"), anthropicTurn("sigB"), anthropicTurn("sigC")}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := NewAnthropic(srv.URL, "key", 1, map[string]Limits{"claude": {Context: 200_000, Output: 32_000}})
	model := Model{Name: "claude"}
	history := []ChatMessage{{Role: RoleSystem, Content: "be brief"}, {Role: RoleUser, Content: "find x"}}

	var reasoning strings.Builder
	a, err := c.Chat(context.Background(), ChatRequest{Model: model, Messages: history, Tools: []ToolSpec{lookup}, ToolChoice: ToolChoiceAuto, ParallelToolCalls: true}, func(d Delta) { reasoning.WriteString(d.Reasoning) })
	if err != nil {
		t.Fatal(err)
	}
	if a.FinishReason != "tool_calls" || len(a.Message.ToolCalls) != 2 || a.Message.ToolCalls[1].Arguments != `{"q":"call2"}` {
		t.Fatalf("first response %+v", a)
	}
	if reasoning.String() != "look it up" || a.Message.ReasoningContent != "look it up" {
		t.Errorf("reasoning streamed %q, kept %q", reasoning.String(), a.Message.ReasoningContent)
	}
	if want := (Usage{PromptTokens: 18, CachedPromptTokens: 5, CacheWritePromptTokens: 3, CompletionTokens: 20}); a.Usage != want || !a.UsageReported {
		t.Errorf("usage %+v, want %+v", a.Usage, want)
	}
	first := fake.bodies[0]
	thinking := first["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["block_binding"].(map[string]any)["prefix_mismatch_behavior"] != "drop_block" {
		t.Errorf("thinking %v", thinking)
	}
	if !strings.Contains(fake.headers[0].Get("anthropic-beta"), "thinking-binding-controls-2026-08-01") {
		t.Errorf("beta header %q", fake.headers[0].Get("anthropic-beta"))
	}
	if first["max_tokens"].(float64) != 32_000 || first["cache_control"] == nil || first["system"].([]any)[0].(map[string]any)["text"] != "be brief" {
		t.Errorf("request %v", first)
	}

	// The parallel results go back in one user turn, after the response replayed with its signed thinking.
	history = append(history, ChatMessage{Role: RoleAssistant, Native: a.Message.Native},
		ChatMessage{Role: RoleTool, ToolCallID: "call1", Content: "x is 1"}, ChatMessage{Role: RoleTool, ToolCallID: "call2", Content: "x is 2"})
	grown := []ToolSpec{lookup, {Name: "fetch", Parameters: map[string]any{"type": "object"}}}
	if _, err := c.Chat(context.Background(), ChatRequest{Model: model, Messages: history, Tools: []ToolSpec{lookup}, ToolChoice: ToolChoiceAuto}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(blocks(fake.bodies[1])), "[[text] [thinking:sigA tool_use tool_use] [tool_result tool_result]]"; got != want {
		t.Errorf("second request %s, want %s", got, want)
	}

	// A grown tool list makes sigA stale, so it is left out.
	if _, err := c.Chat(context.Background(), ChatRequest{Model: model, Messages: history, Tools: grown, ToolChoice: ToolChoiceAuto}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(blocks(fake.bodies[2])), "[[text] [tool_use tool_use] [tool_result tool_result]]"; got != want {
		t.Errorf("request after the tool list grew %s, want %s", got, want)
	}
}

func TestAnthropicKeepsThinkingAfterTheLastChange(t *testing.T) {
	fake := &fakeAnthropic{streams: [][]string{anthropicTurn("sigA", "call1"), anthropicTurn("sigB", "call2"), anthropicTurn("sigC")}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := NewAnthropic(srv.URL, "key", 1, map[string]Limits{"claude": {Context: 200_000, Output: 32_000}})
	grown := []ToolSpec{lookup, {Name: "fetch", Parameters: map[string]any{"type": "object"}}}
	history := []ChatMessage{{Role: RoleUser, Content: "find x"}}
	ask := func(tools []ToolSpec) ChatResponse {
		t.Helper()
		resp, err := c.Chat(context.Background(), ChatRequest{Model: Model{Name: "claude"}, Messages: history, Tools: tools, ToolChoice: ToolChoiceAuto}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	a := ask([]ToolSpec{lookup})
	history = append(history, ChatMessage{Role: RoleAssistant, Native: a.Message.Native}, ChatMessage{Role: RoleTool, ToolCallID: "call1", Content: "1"})
	b := ask(grown)
	history = append(history, ChatMessage{Role: RoleAssistant, Native: b.Message.Native}, ChatMessage{Role: RoleTool, ToolCallID: "call2", Content: "2"})
	ask(grown)
	if got, want := fmt.Sprint(blocks(fake.bodies[2])), "[[text] [tool_use] [tool_result] [thinking:sigB tool_use] [tool_result]]"; got != want {
		t.Errorf("third request %s, want %s", got, want)
	}
}

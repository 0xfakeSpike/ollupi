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

type fakeGemini struct {
	mu      sync.Mutex
	streams [][]string
	paths   []string
	bodies  []string
}

func (f *fakeGemini) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.paths, f.bodies = append(f.paths, r.URL.Path), append(f.bodies, string(raw))
	chunks := f.streams[0]
	f.streams = f.streams[1:]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
}

func TestGeminiStreamsAndReplaysSignatures(t *testing.T) {
	fake := &fakeGemini{streams: [][]string{
		{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"look ","thought":true}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"it up","thought":true}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}},"thoughtSignature":"c2lnQQ=="}]},"finishReason":"STOP"}],` +
				`"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`,
		},
		{`{"candidates":[{"content":{"role":"model","parts":[{"text":"x is 1"}]},"finishReason":"STOP"}]}`},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := NewGemini(srv.URL, "key", 1, map[string]Limits{"gemini-pro": {Context: 1_000_000, Output: 64_000}})
	if err != nil {
		t.Fatal(err)
	}
	history := []ChatMessage{
		{Role: RoleSystem, Content: "be brief"},
		{Role: RoleUser, Content: "what is in this picture?", Parts: []ContentPart{{Type: PartImage, MimeType: "image/png", Data: []byte("png")}}},
	}
	var reasoning strings.Builder
	a, err := c.Chat(context.Background(), ChatRequest{Model: Model{Name: "gemini-pro"}, Messages: history, Tools: []ToolSpec{lookup}, ToolChoice: ToolChoiceAuto}, func(d Delta) { reasoning.WriteString(d.Reasoning) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fake.paths[0], "/models/gemini-pro:streamGenerateContent") {
		t.Errorf("path %s", fake.paths[0])
	}
	for _, want := range []string{`"systemInstruction":{"parts":[{"text":"be brief"}]`, `"inlineData":{"data":"cG5n","mimeType":"image/png"}`, `"includeThoughts":true`, `"maxOutputTokens":64000`, `"parametersJsonSchema"`} {
		if !strings.Contains(fake.bodies[0], want) {
			t.Errorf("first request lacks %s: %s", want, fake.bodies[0])
		}
	}
	if reasoning.String() != "look it up" || a.FinishReason != "tool_calls" || len(a.Message.ToolCalls) != 1 {
		t.Fatalf("response %+v, reasoning %q", a, reasoning.String())
	}
	call := a.Message.ToolCalls[0]
	if !strings.HasPrefix(call.ID, geminiCallPrefix) || call.Arguments != `{"q":"x"}` {
		t.Errorf("call %+v", call)
	}
	if want := (Usage{PromptTokens: 12, CachedPromptTokens: 4, CompletionTokens: 5}); a.Usage != want {
		t.Errorf("usage %+v, want %+v", a.Usage, want)
	}

	history = append(history, ChatMessage{Role: RoleAssistant, Native: a.Message.Native, ToolCalls: a.Message.ToolCalls},
		ChatMessage{Role: RoleTool, ToolCallID: call.ID, Content: "x is 1"})
	if _, err := c.Chat(context.Background(), ChatRequest{Model: Model{Name: "gemini-pro"}, Messages: history, Tools: []ToolSpec{lookup}, ToolChoice: ToolChoiceAuto}, nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal([]byte(fake.bodies[1]), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Contents) != 3 {
		t.Fatalf("second request has %d contents: %s", len(body.Contents), fake.bodies[1])
	}
	model, result := string(body.Contents[1]), string(body.Contents[2])
	if !strings.Contains(model, `"thoughtSignature":"c2lnQQ=="`) || !strings.Contains(model, `"text":"look it up"`) {
		t.Errorf("the model turn is not replayed as received: %s", model)
	}
	if want := `{"parts":[{"functionResponse":{"name":"lookup","response":{"output":"x is 1"}}}],"role":"user"}`; result != want {
		t.Errorf("tool result %s, want %s", result, want)
	}
}

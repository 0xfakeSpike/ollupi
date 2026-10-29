package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// anthropicModel answers the first request with signed thinking and a tool call, and every later one with text.
type anthropicModel struct {
	mu   sync.Mutex
	reqs []map[string]any
}

func (m *anthropicModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	n := len(m.reqs)
	m.mu.Unlock()
	events := []string{
		`{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"usage":{"input_tokens":100,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"list them"}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig%d"}}`, n),
		`{"type":"content_block_stop","index":0}`,
	}
	stop := "end_turn"
	if n == 1 {
		events = append(events,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"github__list_pulls","input":{}}}`,
			`{"type":"content_block_stop","index":1}`)
		stop = "tool_use"
	} else {
		events = append(events,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"PR #42 is open."}}`,
			`{"type":"content_block_stop","index":1}`)
	}
	events = append(events, `{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":10}}`, `{"type":"message_stop"}`)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(e), &head)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, e)
	}
}

// signatures lists the thinking signatures a recorded request sends back.
func signatures(body map[string]any) []string {
	var out []string
	for _, m := range body["messages"].([]any) {
		for _, b := range m.(map[string]any)["content"].([]any) {
			if block := b.(map[string]any); block["type"] == "thinking" {
				out = append(out, block["signature"].(string))
			}
		}
	}
	return out
}

func TestThinkingIsReplayedAcrossTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &anthropicModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ag := agent.New(agent.Config{
		Name: "Ollupi", Models: []llm.Model{{Name: "claude", Connection: "anthropic"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Connections: map[string]llm.Client{"anthropic": llm.NewAnthropic(srv.URL, "key", 4, map[string]llm.Limits{"claude": {Context: 200_000, Output: 32_000}})},
		Providers:   []tools.Provider{githubTools{}}, DB: db,
	})
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()
	w, err := ag.AddWorkspace(t.TempDir(), "app")
	if err != nil {
		t.Fatal(err)
	}
	th, err := ag.StartThread(w.ID, "", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(text string, requests int) {
		t.Helper()
		turn, err := ag.StartTurn(th.ID, agent.Input{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		for {
			v, err := db.Thread(th.ID)
			if err != nil {
				t.Fatal(err)
			}
			if i := len(v.Turns) - 1; v.Turns[i].ID == turn.ID && v.Turns[i].Status != repo.TurnInProgress && v.Turns[i].Status != repo.TurnQueued {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("the turn did not end")
			case <-time.After(20 * time.Millisecond):
			}
		}
		fake.mu.Lock()
		got := len(fake.reqs)
		fake.mu.Unlock()
		if got != requests {
			t.Fatalf("after %q the model got %d requests, want %d", text, got, requests)
		}
	}
	ask("which pull requests are open?", 2)
	ask("and which one is oldest?", 3)
	// The stored responses keep their signed thinking, and the transcript stays the same from turn to turn,
	// so every earlier thinking block goes back.
	if got := fmt.Sprint(signatures(fake.reqs[1])); got != "[sig1]" {
		t.Errorf("second request sends back %s, want [sig1]", got)
	}
	if got := fmt.Sprint(signatures(fake.reqs[2])); got != "[sig1 sig2]" {
		t.Errorf("next turn sends back %s, want [sig1 sig2]", got)
	}
}

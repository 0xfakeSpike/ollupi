package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

// noteTools writes a note; it is not read-only, so an untrusted thread asks before each call.
type noteTools struct{ writes *atomic.Int32 }

func (noteTools) Name() (string, string) { return "notes", "Notes." }
func (noteTools) ToolList(context.Context) ([]*tools.Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []*tools.Spec{{Name: "notes__write", Description: "Write a note.", InputSchema: schema}}, nil
}
func (n noteTools) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	n.writes.Add(1)
	return tools.Result{Output: map[string]any{"ok": true}}, nil
}

// callingModel calls one tool first and answers in text after that.
type callingModel struct {
	tool, args string
	mu         sync.Mutex
	reqs       [][]byte
}

func (m *callingModel) requests() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs
}

func (m *callingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	n := len(m.reqs)
	m.mu.Unlock()
	delta, finish := map[string]any{"content": "好的，那就不写了。"}, "stop"
	if n == 1 {
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": m.tool, "arguments": m.args}}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	send(chunk(delta, nil))
	send(chunk(map[string]any{}, finish))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestAMessageTakesThePlaceOfWhatTheTurnWaitsFor(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args string
		waits            repo.TurnStatus
		reason           string
	}{
		{"approval", "notes__write", `{"text":"buy milk"}`, repo.TurnWaitingOnApproval, "instead of approving"},
		{"question", userinput.Tool, `{"questions":[{"id":"color","header":"Color","question":"Which color?","options":[{"label":"Red"},{"label":"Blue"}]}]}`, repo.TurnWaitingOnUserInput, "instead of answering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			fake := &callingModel{tool: tc.tool, args: tc.args}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			db, err := repo.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			writes := &atomic.Int32{}
			ag := orchestrator.New(orchestrator.Config{
				Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
				Settings:    permission.Settings{ApprovalPolicy: string(permission.Untrusted)},
				Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
				Providers:   []tools.Provider{noteTools{writes}}, DB: db,
			})
			actx, stop := context.WithCancel(ctx)
			if err := ag.Start(actx); err != nil {
				t.Fatal(err)
			}
			defer ag.Wait()
			defer stop()
			w, err := ag.AddWorkspace(ctx, repo.DefaultUser, t.TempDir(), "app")
			if err != nil {
				t.Fatal(err)
			}
			th, err := ag.StartThread(repo.DefaultUser, w.ID, "", "", permission.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			turn, err := ag.StartTurn(ctx, repo.DefaultUser, th.ID, orchestrator.Input{Text: "记一下买牛奶"})
			if err != nil {
				t.Fatal(err)
			}
			until := func(want repo.TurnStatus) {
				for {
					row, err := db.Turn(turn.ID)
					if err == nil && row.Status == want {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatalf("the turn never reached %s: %s", want, row.Status)
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
			until(tc.waits)
			if _, err := ag.SteerTurn(ctx, repo.DefaultUser, th.ID, turn.ID, orchestrator.Input{Text: "算了，先别记"}); err != nil {
				t.Fatal(err)
			}
			until(repo.TurnCompleted)

			if n := writes.Load(); n != 0 {
				t.Errorf("the tool ran %d times after the user steered away from it", n)
			}
			if reqs, err := ag.PendingRequests(repo.DefaultUser, th.ID); err != nil || len(reqs) != 0 {
				t.Errorf("pending requests after the steer: %d, %v", len(reqs), err)
			}
			items, err := db.TurnItems(turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				if it.IsCall() && (it.Status != repo.ItemDeclined || !strings.Contains(it.Error, tc.reason)) {
					t.Errorf("the call it waited on ended %s: %q", it.Status, it.Error)
				}
			}
			reqs := fake.requests()
			if len(reqs) != 2 {
				t.Fatalf("model requests: %d, want 2", len(reqs))
			}
			var second struct {
				Messages []struct{ Role, Content string } `json:"messages"`
			}
			if err := json.Unmarshal(reqs[1], &second); err != nil {
				t.Fatal(err)
			}
			msgs := second.Messages
			if len(msgs) < 2 || msgs[len(msgs)-2].Role != "tool" || !strings.Contains(msgs[len(msgs)-2].Content, tc.reason) ||
				msgs[len(msgs)-1].Role != "user" || !strings.Contains(msgs[len(msgs)-1].Content, "算了，先别记") {
				t.Errorf("the model was not told the call was declined and then given the message: %+v", msgs)
			}
		})
	}
}

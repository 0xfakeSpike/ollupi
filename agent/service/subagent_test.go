package service_test

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
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// slowTools runs a download that ends when released.
type slowTools struct {
	release chan struct{}
}

func (slowTools) Name() (string, string) { return "files", "Downloads files." }
func (slowTools) ToolList(context.Context) ([]*tools.Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []*tools.Spec{{Name: "files__download", Description: "Download a file.", InputSchema: schema}}, nil
}
func (s slowTools) Invoke(ctx context.Context, _ tools.Caller, _ string, args map[string]any) (tools.Result, error) {
	select {
	case <-s.release:
		return tools.Result{Output: map[string]any{"saved": args["url"]}}, nil
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
}

// delegatingModel launches a subagent from the parent, which downloads a file and reports it saved.
type delegatingModel struct {
	mu            sync.Mutex
	parent, child int
	parentLast    string
}

func (m *delegatingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	isChild := strings.Contains(string(body), "You are a subagent: another agent launched you")
	var n int
	if isChild {
		m.child++
		n = m.child
	} else {
		m.parent++
		n, m.parentLast = m.parent, string(body)
	}
	m.mu.Unlock()
	call := func(name, args string) map[string]any {
		return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%v_%d", isChild, n), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
	}
	delta, finish := map[string]any{"content": "都好了。"}, "stop"
	switch {
	case !isChild && n == 1:
		delta, finish = call(subagent.General, `{"description":"Download it","prompt":"Download https://example.com/big.iso and report where it is."}`), "tool_calls"
	case isChild && n == 1:
		delta, finish = call("files__download", `{"url":"https://example.com/big.iso"}`), "tool_calls"
	case isChild:
		delta = map[string]any{"content": "big.iso is saved."}
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

// A parent turn whose subagent still works waits for it rather than ending, answers a message meanwhile and waits
// again, and ends once the subagent's report came back.
func TestAParentAnswersWhileItsSubagentWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	slow := slowTools{release: make(chan struct{})}
	fake := &delegatingModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ag, store := started(ctx, t, service.Config{Providers: []tools.Provider{slow}}, srv.URL)
	th, err := ag.StartThread(db.DefaultUser, "", "", "delegate", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := ag.StartTurn(ctx, db.DefaultUser, th.ID, service.Input{Text: "让子 agent 去下载"})
	if err != nil {
		t.Fatal(err)
	}
	await := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s never happened", what)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	parentStatus := func() db.TurnStatus {
		row, _ := store.Turn(turn.ID)
		return row.Status
	}
	await("the subagent's download", func() bool {
		running, err := store.RunningSubagents(turn.ID)
		if err != nil || len(running) == 0 {
			return false
		}
		current, _ := store.ActiveTurn(running[0])
		return current.Status == db.TurnInProgress
	})
	await("the parent's wait for its subagent", func() bool { return parentStatus() == db.TurnWaitingOnAgent })

	fake.mu.Lock()
	asked := fake.parent
	fake.mu.Unlock()
	if _, err := ag.SteerTurn(ctx, db.DefaultUser, th.ID, turn.ID, service.Input{Text: "下载到哪一步了？"}); err != nil {
		t.Fatal(err)
	}
	await("the parent's answer to the message", func() bool {
		fake.mu.Lock()
		answered := fake.parent > asked && strings.Contains(fake.parentLast, "下载到哪一步了？")
		fake.mu.Unlock()
		return answered && parentStatus() == db.TurnWaitingOnAgent
	})

	close(slow.release)
	await("the parent's end", func() bool { return parentStatus() == db.TurnCompleted })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.parentLast, "big.iso is saved.") {
		t.Errorf("the parent ended without the subagent's report: %s", fake.parentLast)
	}
}

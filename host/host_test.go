package host

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type model struct{ mu sync.Mutex }

func (*model) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 8_000}, nil
}

func (m *model) Chat(_ context.Context, req llm.ChatRequest, _ func(llm.Delta)) (llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	system, last := req.Messages[0].Content, req.Messages[len(req.Messages)-1]
	reply := func(text string, calls ...llm.ToolCall) (llm.ChatResponse, error) {
		return llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: text, ToolCalls: calls}, FinishReason: "stop"}, nil
	}
	switch {
	case system == memory.Instructions:
		var source string
		for _, part := range strings.Split(last.Content, `"item":"`)[1:] {
			source = part[:strings.Index(part, `"`)]
			break
		}
		thread := strings.Split(strings.Split(last.Content, `"threadId":"`)[1], `"`)[0]
		return reply(`{"operations":[{"action":"add","scope":"global","topic":"weather","kind":"preference","text":"Likes rain.","sources":[{"thread":"` +
			thread + `","item":"` + source + `"}]}]}`)
	case !strings.Contains(system, "You are Ollupi"):
		if last.Role == llm.RoleTool {
			return reply("it rains")
		}
		return reply("", llm.ToolCall{ID: "w", Name: "weather", Arguments: "{}"})
	case strings.Contains(last.Content, "it rains"):
		return reply("The subagent says it rains, which you like.")
	case last.Role == llm.RoleTool:
		return reply("A subagent is looking.")
	}
	return reply("", llm.ToolCall{ID: "s", Name: string(agent.GeneralSub), Arguments: `{"description":"weather","prompt":"read the weather"}`})
}

type weather struct{}

func (weather) Name() (string, string) { return "weather", "Reads the weather." }
func (weather) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{Name: "weather", InputSchema: map[string]any{"type": "object"}, Annotations: tool.Annotations{ReadOnlyHint: true, OpenWorldHint: new(false)}}}, nil
}
func (weather) Invoke(context.Context, tool.Call) (kit.Result, error) {
	return kit.Result{Output: "rain"}, nil
}

func TestAHostRunsATurnThroughASubagentAndLearnsFromIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := &model{}
	learner := llm.Model{Name: "m", Connection: "main"}
	h, err := New(ctx, Config{
		Home: t.TempDir(), Name: "Ollupi",
		Users:   []UserModels{{User: "u", Models: []llm.Model{learner}, Connections: map[string]llm.Client{"main": m}, Memory: &learner}},
		Tools:   []kit.Provider{weather{}},
		Boxes:   func(string) (sandbox.Box, error) { return sandbox.Here() },
		Gateway: &gateway.Options{Authenticate: func(context.Context, string) (string, error) { return "u", nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	learned := make(chan memory.Memory, 1)
	ended := make(chan conversation.Ending, 4)
	sub := h.Store.Feed().Subscribe("u", func(e store.Event) {
		if e.Owned != nil && e.Owned.Memory != nil {
			learned <- *e.Owned.Memory
		}
		if e.Change == nil || e.Change.Header == nil || e.Change.Header.Parent != "" {
			return
		}
		for _, turn := range e.Change.Turns {
			if turn.Ended != nil {
				ended <- *turn.Ended
			}
		}
	})
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()

	if err := h.App.EnsureUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	th, err := h.App.StartThread(ctx, "u", "", "", "weather")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.App.Send(ctx, "u", th.ID, app.Input{Text: "what is the weather?"}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ended:
		if e.Status != conversation.Completed {
			t.Fatalf("the turn ended %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("the turn did not end")
	}
	var v conversation.View
	h.Store.Read(ctx, func(r store.Reader) (err error) { v, err = r.View(th.ID); return err })
	if last := v.Items[len(v.Items)-1]; last.Text != "The subagent says it rains, which you like." {
		t.Errorf("the answer %+v", last)
	}
	select {
	case n := <-learned:
		if n.Text != "Likes rain." || n.Scope != memory.Global {
			t.Errorf("learned %+v", n)
		}
	case <-ctx.Done():
		t.Fatal("nothing was learned")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the host stopped with %v", err)
	}
}

func TestAHostRefusesPermissionsItCannotRead(t *testing.T) {
	_, err := New(context.Background(), Config{Home: t.TempDir(), Name: "Ollupi",
		Permissions: policy.Settings{ApprovalPolicy: "sometimes"},
		Boxes:       func(string) (sandbox.Box, error) { return sandbox.Here() }})
	if err == nil || !strings.Contains(err.Error(), "approval policy") {
		t.Errorf("made with %v", err)
	}
}

func TestAUserNeverBorrowsAnotherUsersModelsOrLearningModel(t *testing.T) {
	client := &model{}
	m := llm.Model{Name: "m", Connection: "main"}
	ms, err := newModels(nil, []UserModels{{User: "a", Models: []llm.Model{m}, Connections: map[string]llm.Client{"main": client}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ms.Models("b"); len(got) != 0 {
		t.Fatalf("user b borrowed models: %+v", got)
	}
	if _, err := ms.Limits("b", m); err == nil {
		t.Fatal("user b borrowed another user's model limits")
	}
	other := llm.Model{Name: "other", Connection: "other"}
	if _, err := newModels(nil, []UserModels{{
		User: "a", Models: []llm.Model{m}, Connections: map[string]llm.Client{"main": client}, Memory: &other,
	}}); err == nil {
		t.Fatal("a learning model outside the user's connections was accepted")
	}
}

func TestReplacingUsersModelsTakesEffectWithoutARestart(t *testing.T) {
	client := &model{}
	old := llm.Model{Name: "old", Connection: "a"}
	ms, err := newModels(nil, []UserModels{{User: "u", Models: []llm.Model{old}, Connections: map[string]llm.Client{"a": client}}})
	if err != nil {
		t.Fatal(err)
	}
	next := llm.Model{Name: "next", Connection: "b"}
	if err := ms.set([]UserModels{{User: "u", Models: []llm.Model{next}, Connections: map[string]llm.Client{"b": client}, Memory: &next}}); err != nil {
		t.Fatal(err)
	}
	if got := ms.Models("u"); len(got) != 1 || got[0].Connection != "b" {
		t.Fatalf("models = %+v", got)
	}
	if _, err := ms.Client("u", old); err == nil {
		t.Fatal("a removed model still has a client")
	}
	if m, ok := ms.memory("u"); !ok || m.Connection != "b" {
		t.Fatalf("learning model = %+v, %v", m, ok)
	}
	if err := ms.set([]UserModels{{User: "u", Models: []llm.Model{next}}}); err == nil {
		t.Fatal("a model without a client was accepted")
	}
	if got := ms.Models("u"); len(got) != 1 || got[0].Connection != "b" {
		t.Fatalf("a refused replacement changed the models: %+v", got)
	}
}

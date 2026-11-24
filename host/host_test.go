package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
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
	h, err := New(ctx, Config{
		Home: t.TempDir(), Name: "Ollupi",
		Models:  []account.ModelProvider{provider("main", "m")},
		Dial:    func(account.ModelProvider, account.ProviderModel) (llm.Client, error) { return m, nil },
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
	learn := "main/m"
	if _, err := h.App.UpdateSettings(ctx, "u", app.SettingsChange{LearnModel: &learn}); err != nil {
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

// testModels are a host's models over a fresh store holding users a and b,
// every client dialled counted.
func testModels(t *testing.T, catalog ...account.ModelProvider) (*models, *app.App, *int) {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dialled := new(int)
	ms, err := newModels(context.Background(), st, catalog, func(account.ModelProvider, account.ProviderModel) (llm.Client, error) {
		*dialled++
		return &model{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, ms, nil, nil, nil, nil, cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})
	for _, u := range []string{"a", "b"} {
		if err := a.EnsureUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	return ms, a, dialled
}

func provider(id string, names ...string) account.ModelProvider {
	p := account.ModelProvider{ID: id, API: account.OpenAI, APIKey: "sk-" + id}
	for _, n := range names {
		p.Models = append(p.Models, account.ProviderModel{Name: n, Limits: llm.Limits{Context: 100_000, Output: 4_000}})
	}
	return p
}

func TestEachUserChoosesAmongTheSameModels(t *testing.T) {
	ms, a, _ := testModels(t, provider("main", "x", "y"))
	ctx := context.Background()
	start, learn := "main/y", "main/x"
	if _, err := a.UpdateSettings(ctx, "a", app.SettingsChange{Model: &start, LearnModel: &learn}); err != nil {
		t.Fatal(err)
	}
	if _, first, _ := a.Models(ctx, "a"); first != "main/y" {
		t.Fatalf("a starts with %q", first)
	}
	if models, first, _ := a.Models(ctx, "b"); len(models) != 2 || first != "main/x" {
		t.Fatalf("b has %+v and starts with %q", models, first)
	}
	if m, ok := ms.memory("a"); !ok || m.Connection != "main/x" {
		t.Fatalf("a learns with %+v, %v", m, ok)
	}
	if _, ok := ms.memory("b"); ok {
		t.Fatal("b learns without having chosen a model")
	}
	other := "elsewhere/z"
	if _, err := a.UpdateSettings(ctx, "a", app.SettingsChange{Model: &other}); !errors.Is(err, account.ErrUnknownModel) {
		t.Fatalf("chose a model the deployment does not serve: %v", err)
	}
}

func TestReplacingTheModelsTakesEffectAtOnce(t *testing.T) {
	ms, a, dialled := testModels(t, provider("old", "x"), provider("keep", "k"))
	ctx := context.Background()
	start, learn := "old/x", "old/x"
	if _, err := a.UpdateSettings(ctx, "a", app.SettingsChange{Model: &start, LearnModel: &learn}); err != nil {
		t.Fatal(err)
	}
	kept, err := ms.Client("a", llm.Model{Connection: "keep/k"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.set([]account.ModelProvider{provider("new", "y"), provider("keep", "k")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.Client("a", llm.Model{Connection: "old/x"}); err == nil {
		t.Fatal("a model no longer served still has a client")
	}
	if c, _ := ms.Client("a", llm.Model{Connection: "keep/k"}); c != kept || *dialled != 3 {
		t.Fatalf("an unchanged model was dialled again (%d dials)", *dialled)
	}
	// A choice the deployment no longer serves falls back to its first model,
	// and learns nothing.
	if _, first, _ := a.Models(ctx, "a"); first != "new/y" {
		t.Fatalf("a starts with %q", first)
	}
	if _, ok := ms.memory("a"); ok {
		t.Fatal("a learns with a model no longer served")
	}
	if err := ms.set([]account.ModelProvider{provider("dup", "m"), provider("dup", "n")}); err == nil {
		t.Fatal("a catalog naming a provider twice was taken")
	}
	if models, _, _ := a.Models(ctx, "a"); len(models) != 2 {
		t.Fatalf("a refused catalog changed the models: %+v", models)
	}
}

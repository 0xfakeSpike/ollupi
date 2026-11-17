package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
)

type model struct {
	mu     sync.Mutex
	answer func(req llm.ChatRequest) llm.ChatResponse
}

func (m *model) Chat(_ context.Context, req llm.ChatRequest, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := m.answer(req)
	if resp.Message.ReasoningContent != "" {
		onDelta(llm.Delta{Reasoning: resp.Message.ReasoningContent})
		time.Sleep(20 * time.Millisecond)
	}
	if resp.Message.Content != "" {
		onDelta(llm.Delta{Content: resp.Message.Content})
	}
	return resp, nil
}

func (m *model) Limits(string) (llm.Limits, error) { return limits, nil }

var limits = llm.Limits{Context: 200_000, Output: 8_000}

type models struct{ m *model }

func (ms models) Limits(llm.Model) (llm.Limits, error)         { return limits, nil }
func (ms models) Client(string, llm.Model) (llm.Client, error) { return ms.m, nil }
func (noFiles) Get(context.Context, string) ([]byte, error)    { return nil, errors.New("no files") }
func noSkills(agent.Name) ([]prompt.Skill, error)              { return nil, nil }
func answer(text string) llm.ChatResponse                      { return reply(text) }
func reply(text string, calls ...llm.ToolCall) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: text, ToolCalls: calls}, FinishReason: "stop"}
}

type noFiles struct{}

type set struct {
	invoke func(c tool.Call) tool.Result
}

var schema = map[string]any{"type": "object"}

func (set) Specs() []tool.Spec {
	return []tool.Spec{
		{Name: agent.ReadFileTool, Provider: agent.WorkspaceProvider, Schema: schema, Annotations: tool.Annotations{ReadOnlyHint: true}},
		{Name: agent.RunCommandTool, Provider: agent.WorkspaceProvider, Schema: schema, Content: true},
		{Name: string(agent.GeneralSub), Provider: agent.SubagentsProvider, Schema: schema, Annotations: tool.Annotations{ReadOnlyHint: true}},
	}
}
func (set) Unavailable() []string { return nil }
func (set) Content(_ context.Context, c tool.Call) (policy.Content, error) {
	command, _ := c.Arguments["command"].(string)
	return policy.Content{Text: command, Match: command}, nil
}
func (set) Prepare(context.Context, tool.Call) (tool.Plan, error) { return tool.Plan{}, nil }
func (s set) Invoke(_ context.Context, c tool.Call) tool.Result   { return s.invoke(c) }

type fixture struct {
	t  *testing.T
	st store.Store
	r  *Runner
	m  *model
}

func setup(t *testing.T, invoke func(tool.Call) tool.Result) *fixture {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &model{}
	tools := func(context.Context, string) (tool.Set, error) { return set{invoke: invoke}, nil }
	r := New(st, models{m}, tools, noFiles{}, noSkills, "Ollupi", "", policy.Settings{ApprovalPolicy: string(policy.OnRequest)}, nil)
	return &fixture{t: t, st: st, r: r, m: m}
}

func (f *fixture) ask(thread, text string) {
	f.t.Helper()
	now := time.Now().UTC()
	f.update(func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: user.Thread{ID: thread, Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append(thread,
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{Model: llm.Model{Name: "m"}, Role: string(agent.FullMain)}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: text}})
		return err
	})
}

func (f *fixture) update(fn func(store.Tx) error) {
	f.t.Helper()
	if err := f.st.Update(context.Background(), fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) run(thread string) {
	f.t.Helper()
	if err := f.r.Run(context.Background(), thread); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) view(thread string) conversation.View {
	f.t.Helper()
	var v conversation.View
	err := f.st.Read(context.Background(), func(r store.Reader) (err error) { v, err = r.View(thread); return err })
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func kinds(v conversation.View) string {
	var out []string
	for _, it := range v.Items {
		out = append(out, string(it.Kind)+":"+string(it.Status))
	}
	return strings.Join(out, ",")
}

func TestATurnReadsAFileAndAnswers(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result { return tool.Result{Output: json.RawMessage(`"hello"`)} })
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		if last := req.Messages[len(req.Messages)-1]; last.Role == llm.RoleTool {
			return answer("it says hello")
		}
		return reply("", llm.ToolCall{ID: "call_1", Name: agent.ReadFileTool, Arguments: `{"path":"a.txt"}`})
	}
	var deltas []string
	f.st.Feed().Subscribe("u", func(e store.Event) {
		if e.Delta != nil {
			deltas = append(deltas, e.Delta.Text)
		}
	})
	f.ask("th", "what does a.txt say?")
	f.run("th")
	v := f.view("th")
	if got := kinds(v); got != "userMessage:completed,toolCall:completed,agentMessage:completed" {
		t.Fatalf("items %s", got)
	}
	if ended := v.Turns[0].Ended; ended == nil || ended.Status != conversation.Completed {
		t.Errorf("the turn %+v", v.Turns[0])
	}
	if strings.Join(deltas, "") != "it says hello" {
		t.Errorf("deltas %q", deltas)
	}
}

func TestAResponseKeepsWhatItUsedAndHowLongItThought(t *testing.T) {
	f := setup(t, nil)
	f.m.answer = func(llm.ChatRequest) llm.ChatResponse {
		resp := answer("Monday")
		resp.Message.ReasoningContent = "weighing the days"
		resp.Usage, resp.UsageReported = llm.Usage{PromptTokens: 100, CompletionTokens: 7}, true
		return resp
	}
	f.ask("th", "which day?")
	f.run("th")
	v := f.view("th")
	var thought conversation.Item
	for _, it := range v.Items {
		if it.Kind == conversation.ItemReasoning {
			thought = it
		}
	}
	if thought.CompletedAt == nil || thought.CompletedAt.Sub(thought.StartedAt) < 20*time.Millisecond {
		t.Errorf("the thinking %+v", thought)
	}
	used := v.Turns[0].Responses
	if len(used) != 1 || used[0].Response != thought.Response || used[0].Usage.InputTokens != 100 || used[0].Stats.BudgetTokens == 0 {
		t.Errorf("the responses %+v", used)
	}
}

func TestACommandWaitsForApprovalAndRunsOnceAccepted(t *testing.T) {
	ran := 0
	f := setup(t, func(c tool.Call) tool.Result { ran++; return tool.Result{Output: json.RawMessage(`"done"`)} })
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		if last := req.Messages[len(req.Messages)-1]; last.Role == llm.RoleTool {
			return answer("cleaned")
		}
		return reply("", llm.ToolCall{ID: "call_1", Name: agent.RunCommandTool, Arguments: `{"command":"rm -rf build"}`})
	}
	f.ask("th", "clean the build")
	f.run("th")
	v := f.view("th")
	if v.Status() != conversation.ThreadWaitingOnApproval || ran != 0 {
		t.Fatalf("status %s, ran %d", v.Status(), ran)
	}
	call := v.Items[1]
	if call.Approval == nil || call.Approval.Content != "rm -rf build" {
		t.Fatalf("the approval %+v", call.Approval)
	}
	f.update(func(tx store.Tx) error {
		_, err := tx.Append("th", conversation.Entry{Turn: "t1", Item: call.ID, Body: conversation.Decided{Decision: conversation.Accept, By: "u"}})
		return err
	})
	f.run("th")
	if v := f.view("th"); v.Status() != conversation.ThreadIdle || ran != 1 {
		t.Errorf("after the approval: %s, ran %d, items %s", v.Status(), ran, kinds(v))
	}
}

func TestASubagentReportsToTheTurnThatLaunchedIt(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result {
		return tool.Result{Launch: &tool.Launch{Role: string(agent.GeneralSub), Title: "count", Task: "count the files"}}
	})
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		last := req.Messages[len(req.Messages)-1]
		switch {
		case !strings.Contains(req.Messages[0].Content, "You are Ollupi"):
			return answer("42 files")
		case strings.Contains(last.Content, "42 files"):
			return answer("there are 42 files")
		case last.Role == llm.RoleTool:
			return answer("a subagent is counting")
		}
		return reply("", llm.ToolCall{ID: "call_1", Name: string(agent.GeneralSub), Arguments: `{"task":"count the files"}`})
	}
	f.ask("th", "how many files?")
	f.run("th")
	v := f.view("th")
	child := v.Items[1].Child
	if child == "" || v.Waiting() != conversation.WaitChildren {
		t.Fatalf("launched %q, waiting %q, items %s", child, v.Waiting(), kinds(v))
	}
	f.run(child)
	v = f.view("th")
	if len(v.Items) < 4 || v.Items[3].Kind != conversation.ItemReport || !strings.Contains(string(v.Items[3].Result.Output), "42 files") || !v.Runnable() {
		t.Fatalf("the report: %s", kinds(v))
	}
	f.run("th")
	if v := f.view("th"); v.Status() != conversation.ThreadIdle || v.Items[len(v.Items)-1].Text != "there are 42 files" {
		t.Errorf("after the report: %s %s", v.Status(), kinds(v))
	}
}

func TestEndingATurnStopsItsSubagentsAndClosesItsCalls(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result {
		return tool.Result{Launch: &tool.Launch{Role: string(agent.GeneralSub), Task: "wait"}}
	})
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		if last := req.Messages[len(req.Messages)-1]; last.Role == llm.RoleTool {
			return reply("", llm.ToolCall{ID: "call_2", Name: agent.RunCommandTool, Arguments: `{"command":"make"}`})
		}
		return reply("", llm.ToolCall{ID: "call_1", Name: string(agent.GeneralSub), Arguments: `{}`})
	}
	f.ask("th", "go")
	f.run("th")
	v := f.view("th")
	child := v.Items[1].Child
	if child == "" || v.Waiting() != conversation.WaitApproval {
		t.Fatalf("launched %q, waiting %q, items %s", child, v.Waiting(), kinds(v))
	}
	f.update(func(tx store.Tx) error { return End(tx, "th", conversation.Interrupted, "interrupted by the user") })
	v = f.view("th")
	if ended := v.Turns[0].Ended; ended == nil || ended.Status != conversation.Interrupted {
		t.Fatalf("the turn %+v", v.Turns[0])
	}
	if got := kinds(v); got != "userMessage:completed,toolCall:completed,toolCall:declined,agentReport:failed" {
		t.Errorf("items %s", got)
	}
	if c := f.view(child); c.Turns[0].Ended == nil || c.Turns[0].Ended.Reason != stoppedWithParent {
		t.Errorf("the subagent's turn %+v", c.Turns[0])
	}
}

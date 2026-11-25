package run

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
)

type model struct {
	mu     sync.Mutex
	answer func(req llm.ChatRequest) llm.ChatResponse
	stream []llm.Delta
}

func (m *model) Chat(_ context.Context, req llm.ChatRequest, onDelta func(llm.Delta)) (llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := m.answer(req)
	if m.stream != nil {
		for _, d := range m.stream {
			onDelta(d)
		}
		return resp, nil
	}
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

func (ms models) Limits(string, llm.Model) (llm.Limits, error) { return limits, nil }
func (ms models) Client(string, llm.Model) (llm.Client, error) { return ms.m, nil }
func (noFiles) Get(context.Context, string) ([]byte, error)    { return nil, errors.New("no files") }
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
		{Name: agent.ReadFileTool, Provider: agent.WorkspaceProvider, Schema: schema, Annotations: tool.Annotations{ReadOnlyHint: true},
			Background: tool.BackgroundOptional},
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
	r := New(st, models{m}, tools, noFiles{}, Deployment{Name: "Ollupi", Permissions: policy.Settings{ApprovalPolicy: string(policy.OnRequest)}})
	return &fixture{t: t, st: st, r: r, m: m}
}

func (f *fixture) ask(thread, text string) {
	f.t.Helper()
	now := time.Now().UTC()
	f.update(func(tx store.Tx) error {
		if err := tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: thread, Created: now, Updated: now}}); err != nil {
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

func TestAnAnswerToAMessageThatWasOvertakenIsDropped(t *testing.T) {
	f := setup(t, nil)
	var asked [][]llm.ChatMessage
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		asked = append(asked, req.Messages)
		if len(asked) == 1 {
			f.update(func(tx store.Tx) error {
				_, err := tx.Append("th", conversation.Entry{Turn: "t1", Item: "m2", Body: conversation.UserMessage{Text: "it is the one at github.com/a/b"}})
				return err
			})
			return answer("I could not tell which project you meant")
		}
		return answer("github.com/a/b is a desktop agent")
	}
	f.ask("th", "which project is agent-desktop?")
	f.run("th")
	v := f.view("th")
	var said []string
	for _, it := range v.Items {
		if it.Kind == conversation.ItemAgentMessage {
			said = append(said, it.Text)
		}
	}
	if len(asked) != 2 || !strings.Contains(asked[1][len(asked[1])-1].Content, "github.com/a/b") {
		t.Fatalf("the second call did not see the new message: %d calls", len(asked))
	}
	if strings.Join(said, " | ") != "github.com/a/b is a desktop agent" || len(v.Turns[0].Responses) != 1 {
		t.Errorf("the answers kept %q, responses %d", said, len(v.Turns[0].Responses))
	}
}

func TestAResponseKeepsWhatItUsedAndHowLongItThought(t *testing.T) {
	f := setup(t, nil)
	f.m.answer = func(llm.ChatRequest) llm.ChatResponse {
		resp := answer("Monday")
		resp.Message.ReasoningContent = "weighing the days"
		resp.Usage = llm.Usage{PromptTokens: 100, CompletionTokens: 7}
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
	if child == "" || v.Waiting() != conversation.WaitReports {
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

func TestASubagentWorksAtThePermissionSettingsOfItsThread(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result {
		return tool.Result{Launch: &tool.Launch{Role: string(agent.GeneralSub), Title: "count", Task: "count the files"}}
	})
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		if last := req.Messages[len(req.Messages)-1]; last.Role == llm.RoleTool {
			return answer("a subagent is counting")
		}
		return reply("", llm.ToolCall{ID: "call_1", Name: string(agent.GeneralSub), Arguments: `{"task":"count the files"}`})
	}
	loose := policy.Settings{ApprovalPolicy: string(policy.Never), Rules: policy.Rules{Allow: []string{"run_command(make)"}}}
	f.update(func(tx store.Tx) error {
		now := time.Now().UTC()
		if err := tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: "th", Settings: loose, Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append("th",
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{Model: llm.Model{Name: "m"}, Role: string(agent.FullMain)}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "how many files?"}})
		return err
	})
	f.run("th")
	child := f.view("th").Items[1].Child
	var th store.Thread
	if err := f.st.Read(context.Background(), func(r store.Reader) (err error) { th, err = r.Thread(child); return err }); err != nil {
		t.Fatal(err)
	}
	if th.Settings.ApprovalPolicy != loose.ApprovalPolicy || !slices.Equal(th.Settings.Rules.Allow, loose.Rules.Allow) {
		t.Errorf("the subagent's permission settings: %+v", th.Settings)
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

func TestASubagentReportsOnlyTheTurnItWasLaunchedFor(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result {
		return tool.Result{Launch: &tool.Launch{Role: string(agent.GeneralSub), Title: "count", Task: "count the files"}}
	})
	launched := false
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		switch {
		case !strings.Contains(req.Messages[0].Content, "You are Ollupi"):
			return answer("counted")
		case !launched:
			launched = true
			return reply("", llm.ToolCall{ID: "call_1", Name: string(agent.GeneralSub), Arguments: `{}`})
		}
		return answer("done")
	}
	f.ask("th", "how many files?")
	f.run("th")
	child := f.view("th").Items[1].Child
	f.run(child)
	f.run("th")
	before := kinds(f.view("th"))
	f.update(func(tx store.Tx) error {
		_, err := tx.Append(child,
			conversation.Entry{Turn: "t2", Body: conversation.TurnQueued{Model: llm.Model{Name: "m"}, Role: string(agent.GeneralSub)}},
			conversation.Entry{Turn: "t2", Item: "m2", Body: conversation.UserMessage{Text: "count again"}})
		return err
	})
	f.run(child)
	if c := f.view(child); len(c.Turns) != 2 || c.Turns[1].Ended == nil || c.Turns[1].Ended.Status != conversation.Completed {
		t.Fatalf("the subagent's second turn %+v", c.Turns)
	}
	if after := kinds(f.view("th")); after != before {
		t.Errorf("the parent heard of the second turn: %s, was %s", after, before)
	}
}

func (f *fixture) tasks() []store.Runnable {
	f.t.Helper()
	var out []store.Runnable
	err := f.st.Read(context.Background(), func(r store.Reader) (err error) { out, err = r.Tasks(10, nil); return err })
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) background() string {
	f.t.Helper()
	f.ask("th", "what does a.txt say?")
	f.run("th")
	v := f.view("th")
	task := v.Items[1].Task
	queued := f.tasks()
	if task == "" || v.Items[1].Status != conversation.Done || len(queued) != 1 || queued[0].ID != task || queued[0].Thread != "th" {
		f.t.Fatalf("launched %q, queued %+v, items %s", task, queued, kinds(v))
	}
	return task
}

func backgroundRead(req llm.ChatRequest) llm.ChatResponse {
	last := req.Messages[len(req.Messages)-1]
	switch {
	case strings.Contains(last.Content, "run in the background: completed"):
		return answer("it says hello")
	case last.Role == llm.RoleTool:
		return answer("reading it in the background")
	}
	return reply("", llm.ToolCall{ID: "call_1", Name: agent.ReadFileTool, Arguments: `{"path":"a.txt","background":true}`})
}

func TestABackgroundCallReportsToTheTurnThatLaunchedIt(t *testing.T) {
	var got tool.Call
	f := setup(t, func(c tool.Call) tool.Result { got = c; return tool.Result{Output: json.RawMessage(`"hello"`)} })
	f.m.answer = backgroundRead
	task := f.background()
	if v := f.view("th"); v.Waiting() != conversation.WaitReports || got.Tool != "" {
		t.Fatalf("waiting %q, ran %+v", v.Waiting(), got)
	}
	if err := f.r.Task(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	v := f.view("th")
	report := v.Items[len(v.Items)-1]
	if report.Kind != conversation.ItemReport || report.Task != task || string(report.Result.Output) != `"hello"` || !v.Runnable() || len(f.tasks()) != 0 {
		t.Fatalf("the report %+v, items %s", report, kinds(v))
	}
	if got.Tool != agent.ReadFileTool || got.Thread != "th" || got.Item != v.Items[1].ID || got.Arguments["path"] != "a.txt" {
		t.Errorf("the call %+v", got)
	}
	f.run("th")
	if v := f.view("th"); v.Status() != conversation.ThreadIdle || v.Items[len(v.Items)-1].Text != "it says hello" {
		t.Errorf("after the report: %s %s", v.Status(), kinds(v))
	}
}

func TestATaskAnEarlierProcessStartedIsReportedCutOff(t *testing.T) {
	ran := false
	f := setup(t, func(c tool.Call) tool.Result { ran = true; return tool.Result{} })
	f.m.answer = backgroundRead
	task := f.background()
	f.update(func(tx store.Tx) error { return tx.StartTask(task, time.Now().UTC()) })
	if err := f.r.Task(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	v := f.view("th")
	if report := v.Items[len(v.Items)-1]; ran || report.Kind != conversation.ItemReport || report.Error != strandedTask {
		t.Errorf("ran %v, the report %+v", ran, report)
	}
}

func TestEndingATurnReportsItsTasks(t *testing.T) {
	f := setup(t, func(c tool.Call) tool.Result { return tool.Result{} })
	f.m.answer = backgroundRead
	task := f.background()
	f.update(func(tx store.Tx) error { return End(tx, "th", conversation.Interrupted, "interrupted by the user") })
	v := f.view("th")
	if got := kinds(v); got != "userMessage:completed,toolCall:completed,agentMessage:completed,agentReport:failed" || len(f.tasks()) != 0 {
		t.Fatalf("items %s, tasks %+v", got, f.tasks())
	}
	if report := v.Items[3]; report.Task != task || report.Error != stoppedWithParent || v.Turns[0].Ended == nil {
		t.Errorf("the report %+v, the turn %+v", report, v.Turns[0])
	}
}

func TestAWorkspaceGivesItsInstructionsAndHidesTheProvidersItTurnsOff(t *testing.T) {
	f := setup(t, nil)
	var system string
	var offered []string
	f.m.answer = func(req llm.ChatRequest) llm.ChatResponse {
		system = req.Messages[0].Content
		for _, tl := range req.Tools {
			offered = append(offered, tl.Name)
		}
		return answer("noted")
	}
	now := time.Now().UTC()
	f.update(func(tx store.Tx) error {
		if err := tx.EnsureUser("u", account.Environment{ID: "here", Name: "This computer", Provider: "local"}); err != nil {
			return err
		}
		w := account.Workspace{ID: "w", Name: "plans", Environment: "here", Root: t.TempDir(), Instructions: "Answer in haiku.",
			Providers: account.Providers{Disabled: []string{agent.SubagentsProvider}}, Created: now, Updated: now}
		if err := tx.PutWorkspace("u", w); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: "th", Workspace: "w", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append("th",
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{Model: llm.Model{Name: "m"}, Role: string(agent.FullMain)}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "plan my week"}})
		return err
	})
	f.run("th")
	if !strings.Contains(system, "# WORKSPACE INSTRUCTIONS") || !strings.Contains(system, "Answer in haiku.") {
		t.Errorf("the system prompt %q", system)
	}
	if slices.Contains(offered, string(agent.GeneralSub)) || !slices.Contains(offered, agent.ReadFileTool) {
		t.Errorf("offered %q", offered)
	}
}

func TestDeltasSayWhereTheyGoInTheFinalText(t *testing.T) {
	f := setup(t, nil)
	f.m.stream = []llm.Delta{{Reasoning: "thinking "}, {Reasoning: "再想😀", Content: "Hi "}, {Content: "😀 there"}}
	f.m.answer = func(llm.ChatRequest) llm.ChatResponse {
		resp := answer("Hi 😀 there")
		resp.Message.ReasoningContent = "thinking 再想😀"
		return resp
	}
	built := map[string][]uint16{}
	f.st.Feed().Subscribe("u", func(e store.Event) {
		d := e.Delta
		if d == nil {
			return
		}
		if d.Offset != len(built[d.Item]) {
			t.Errorf("a delta of %s at %d, after %d units", d.Item, d.Offset, len(built[d.Item]))
		}
		built[d.Item] = append(built[d.Item], utf16.Encode([]rune(d.Text+d.Reasoning))...)
	})
	f.ask("th", "hi")
	f.run("th")
	for _, it := range f.view("th").Items {
		if it.Kind != conversation.ItemAgentMessage && it.Kind != conversation.ItemReasoning {
			continue
		}
		if got := string(utf16.Decode(built[it.ID])); got != it.Text {
			t.Errorf("the deltas of the %s build %q, not %q", it.Kind, got, it.Text)
		}
	}
}

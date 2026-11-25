package hub

import (
	"context"
	"encoding/json"
	"github.com/0xfakeSpike/ollupi/environment"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/blob"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

func noStatuses(string) ([]account.MCPStatus, error) { return nil, nil }

// here are the environments of a deployment that offers this machine, its
// environments folders of a temporary one.
func here(t testing.TB, st store.Store) *environment.Boxes {
	t.Helper()
	envs, err := environment.New(st, map[string]sandbox.Provider{"local": sandbox.Local{Dir: t.TempDir(), Config: sandbox.Config{Mode: string(sandbox.ModeFullAccess)}}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { envs.Close() })
	return envs
}

type fixture struct {
	t   *testing.T
	ctx context.Context
	st  store.Store
	a   *app.App
	p   *Provider
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &Provider{St: st}
	tools := func(ctx context.Context, _ string) (tool.Set, error) { return kit.New(ctx, nil, p) }
	a := app.New(st, catalog{{ID: "main", API: account.OpenAI, Models: []account.ProviderModel{{Name: "big", Limits: llm.Limits{Context: 200_000, Output: 8_000}}}}}, tools, noStatuses, here(t, st), blob.Dir(t.TempDir()), cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})
	p.App = a
	f := &fixture{t: t, ctx: context.Background(), st: st, a: a, p: p}
	if err := a.EnsureUser(f.ctx, "u"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) workspace(name string) account.Workspace {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	os.Mkdir(dir, 0o755)
	w, err := f.a.AddWorkspace(f.ctx, "u", "", dir, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return w
}

func (f *fixture) call(from, name string, args map[string]any) (json.RawMessage, error) {
	f.t.Helper()
	var th store.Thread
	if err := f.st.Read(f.ctx, func(r store.Reader) (err error) { th, err = r.Thread(from); return err }); err != nil {
		f.t.Fatal(err)
	}
	res, err := f.p.Invoke(f.ctx, tool.Call{Caller: tool.Caller{User: "u", Thread: from, Workspace: th.Workspace}, Tool: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(res.Output)
	return out, nil
}

func (f *fixture) recordStart(hub string, out json.RawMessage) {
	f.t.Helper()
	err := f.st.Update(f.ctx, func(tx store.Tx) error {
		_, err := tx.Append(hub,
			conversation.Entry{Turn: "h1", Body: conversation.TurnQueued{Model: llm.Model{Name: "big"}, Role: string(agent.HubMain)}},
			conversation.Entry{Turn: "h1", Item: "hm", Body: conversation.UserMessage{Text: "write the release notes in docs"}},
			conversation.Entry{Turn: "h1", Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: "h1", Item: "hc", Body: conversation.ToolCall{Tool: agent.StartConversationTool, Response: "r"}},
			conversation.Entry{Turn: "h1", Item: "hc", Body: conversation.ToolStarted{}},
			conversation.Entry{Turn: "h1", Item: "hc", Body: conversation.ToolResult{Output: out}})
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestTheHubHandsOutWorkAndFollowsIt(t *testing.T) {
	f := setup(t)
	home, docs := f.workspace("home"), f.workspace("docs")
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace); err != nil {
		t.Fatal(err)
	}
	hub, err := f.a.StartThread(f.ctx, "u", home.ID, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.call(hub.ID, agent.ListWorkspacesTool, map[string]any{})
	if err != nil || !strings.Contains(string(out), `"kind":"hub"`) || !strings.Contains(string(out), `"kind":"project"`) {
		t.Fatalf("the workspaces %s, %v", out, err)
	}
	if _, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": home.ID, "title": "x", "message": "x"}); err == nil {
		t.Error("work handed to the hub itself")
	}
	out, err = f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": docs.ID, "title": "release notes", "message": "write the release notes"})
	if err != nil {
		t.Fatal(err)
	}
	var started Started
	json.Unmarshal(out, &started)
	var th store.Thread
	var v conversation.View
	f.st.Read(f.ctx, func(r store.Reader) (err error) {
		if th, err = r.Thread(started.Conversation); err != nil {
			return err
		}
		v, err = r.View(th.ID)
		return err
	})
	first := v.Items[0]
	if th.Workspace != docs.ID || th.Parent != "" || v.Turns[0].Role != string(agent.FullMain) || first.Text != "write the release notes" {
		t.Fatalf("the conversation %+v, role %s, first message %q", th.Thread, v.Turns[0].Role, first.Text)
	}
	if first.From == nil || first.From.Thread != hub.ID || first.From.Title != "plan" {
		t.Errorf("the first message came from %+v", first.From)
	}
	r, err := prompt.NewWindow("big", llm.Limits{Context: 200_000, Output: 8_000}).Build(prompt.Context{Items: prompt.Transcript(v), System: "sys"})
	if err != nil || !strings.Contains(r.Messages[0].Content, `by the hub conversation "plan" on the user's behalf]`+"\nwrite the release notes") {
		t.Errorf("the model reads %q, %v", r.Messages[0].Content, err)
	}
	f.recordStart(hub.ID, out)
	if _, err := f.call(hub.ID, agent.SendMessageTool, map[string]any{"conversation": hub.ID, "message": "x"}); err == nil {
		t.Error("a message sent to a hub conversation")
	}
	out, err = f.call(hub.ID, agent.SendMessageTool, map[string]any{"conversation": th.ID, "message": "and the changelog"})
	if err != nil || !strings.Contains(string(out), `"queued":true`) {
		t.Fatalf("the follow-up %s, %v", out, err)
	}
	for _, args := range []map[string]any{{}, {"workspace": docs.ID}, {"conversations": []any{th.ID}}} {
		out, err = f.call(hub.ID, agent.ListConversationsTool, args)
		var got Conversations
		json.Unmarshal(out, &got)
		listed := got.Conversations
		if err != nil || len(listed) != 1 || listed[0].ID != th.ID || listed[0].Status != conversation.ThreadActive || listed[0].Queued != 1 {
			t.Errorf("listed with %v: %s, %v", args, out, err)
		}
	}
	nextWeek, err := f.a.StartThread(f.ctx, "u", home.ID, "", "next week")
	if err != nil {
		t.Fatal(err)
	}
	out, err = f.call(nextWeek.ID, agent.ListConversationsTool, map[string]any{})
	if err != nil || !strings.Contains(string(out), th.ID) {
		t.Errorf("another hub conversation lists %s, %v", out, err)
	}
	out, err = f.call(hub.ID, agent.ReadConversationTool, map[string]any{"conversation": th.ID})
	if err != nil || !strings.Contains(string(out), `"text":"and the changelog","from":"`+hub.ID+`"`) || !strings.Contains(string(out), "write the release notes") {
		t.Errorf("read %s, %v", out, err)
	}
}

func TestTheHubKeepsItsTodosInTheApp(t *testing.T) {
	f := setup(t)
	specs, err := f.p.Tools(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
		if s.Name == agent.StartConversationTool {
			args, ok := s.InputSchema.(*jsonschema.Schema)
			if !ok || args.Properties["todo"] == nil {
				t.Errorf("%s does not take a todo: %v", s.Name, s.InputSchema)
			}
		}
	}
	want := []string{agent.ListWorkspacesTool, agent.StartConversationTool, agent.SendMessageTool,
		agent.ListConversationsTool, agent.ReadConversationTool, agent.AddTodoTool, agent.UpdateTodoTool, agent.ListTodosTool}
	for _, name := range want {
		if !slices.Contains(names, name) {
			t.Errorf("the hub offers %q, not %q", names, name)
		}
	}
	if len(names) != len(want) {
		t.Errorf("the hub's tools %q", names)
	}
}

// catalog is a deployment's fixed models.
type catalog []account.ModelProvider

func (c catalog) Providers() []account.ModelProvider { return c }

func TestAHandOffStartsLinksAndSendsTogetherOrNotAtAll(t *testing.T) {
	f := setup(t)
	home, docs := f.workspace("home"), f.workspace("docs")
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace); err != nil {
		t.Fatal(err)
	}
	hub, err := f.a.StartThread(f.ctx, "u", home.ID, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	todo, err := f.a.AddTodo(f.ctx, "u", "release notes", "")
	if err != nil {
		t.Fatal(err)
	}
	inDocs := func() []store.Thread {
		threads, _, _, err := f.a.Threads(f.ctx, "u", store.ThreadFilter{Workspace: &docs.ID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return threads
	}
	if _, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": docs.ID, "title": "notes", "message": " ", "todo": todo.ID}); err == nil {
		t.Fatal("a hand-off with a blank message was taken")
	}
	if left, _ := f.a.Todo(f.ctx, "u", todo.ID); len(inDocs()) != 0 || left.Status != account.TodoOpen || left.Thread != "" {
		t.Fatalf("a refused hand-off left %d conversations and the todo %+v", len(inDocs()), left)
	}
	out, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": docs.ID, "title": "notes", "message": "write them", "todo": todo.ID})
	if err != nil {
		t.Fatal(err)
	}
	var started Started
	json.Unmarshal(out, &started)
	linked, _ := f.a.Todo(f.ctx, "u", todo.ID)
	if threads := inDocs(); len(threads) != 1 || threads[0].ID != started.Conversation || linked.Status != account.TodoHanded ||
		linked.Thread != started.Conversation || started.Todo == nil || started.Todo.Status != "handed" {
		t.Errorf("handed off %s; the todo %+v", out, linked)
	}
}

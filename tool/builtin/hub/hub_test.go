package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
)

type models struct{}

func (models) Models() []llm.Model { return []llm.Model{{Name: "big", Connection: "main"}} }
func (models) Limits(llm.Model) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 8_000}, nil
}
func noStatuses(string) ([]account.MCPStatus, error) { return nil, nil }
func here(string) (sandbox.Box, error)               { return sandbox.Here() }

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
	a := app.New(st, models{}, tools, noStatuses, here, blob.Dir(t.TempDir()), cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})
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
	w, err := f.a.AddWorkspace(f.ctx, "u", dir, name)
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
	first := v.Items[0].Text
	if th.Workspace != docs.ID || th.Parent != "" || v.Turns[0].Role != string(agent.FullMain) || !strings.HasPrefix(first, `[Sent by the hub conversation "plan"`) {
		t.Fatalf("the conversation %+v, role %s, first message %q", th.Thread, v.Turns[0].Role, first)
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
	if err != nil || !strings.Contains(string(out), "and the changelog") || !strings.Contains(string(out), "write the release notes") {
		t.Errorf("read %s, %v", out, err)
	}
}

func TestTodosHoldThePlanAndFollowTheirConversations(t *testing.T) {
	f := setup(t)
	home, docs := f.workspace("home"), f.workspace("docs")
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace); err != nil {
		t.Fatal(err)
	}
	hub, err := f.a.StartThread(f.ctx, "u", home.ID, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	todo := func(name string, args map[string]any) (Todo, error) {
		t.Helper()
		out, err := f.call(hub.ID, name, args)
		var got Todo
		json.Unmarshal(out, &got)
		return got, err
	}
	notes, err := todo(agent.AddTodoTool, map[string]any{"title": "write the release notes"})
	if err != nil || notes.ID != 1 || notes.Status != account.TodoOpen {
		t.Fatalf("the first todo %+v, %v", notes, err)
	}
	review, _ := todo(agent.AddTodoTool, map[string]any{"title": "review the docs"})
	if _, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": docs.ID, "title": "x", "message": "x", "todo": 99}); err == nil {
		t.Error("a conversation started for a todo that is not there")
	}
	out, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{"workspace": docs.ID, "title": "release notes", "message": "write them", "todo": notes.ID})
	var started Started
	json.Unmarshal(out, &started)
	if err != nil || started.Todo == nil || started.Todo.Status != account.TodoHanded || started.Todo.Conversation != started.Conversation || started.Todo.Workspace != docs.ID {
		t.Fatalf("started %s, %v", out, err)
	}
	if threads, _, _ := f.a.Threads(f.ctx, "u", store.ThreadFilter{Workspace: &docs.ID, Limit: 10}); len(threads) != 1 {
		t.Errorf("docs has %d conversations", len(threads))
	}
	done, err := todo(agent.UpdateTodoTool, map[string]any{"id": notes.ID, "status": "done", "result": "published"})
	if err != nil || done.Status != account.TodoDone || done.Done == "" || done.Result != "published" || done.Conversation != started.Conversation {
		t.Errorf("done %+v, %v", done, err)
	}
	linked, err := todo(agent.UpdateTodoTool, map[string]any{"id": review.ID, "conversation": started.Conversation})
	if err != nil || linked.Workspace != docs.ID || linked.Status != account.TodoOpen {
		t.Errorf("linked %+v, %v", linked, err)
	}
	for _, c := range []struct {
		args map[string]any
		ids  []int64
	}{{map[string]any{}, []int64{review.ID}}, {map[string]any{"statuses": []any{"done"}}, []int64{notes.ID}}} {
		out, err := f.call(hub.ID, agent.ListTodosTool, c.args)
		var got Todos
		json.Unmarshal(out, &got)
		var ids []int64
		for _, td := range got.Todos {
			ids = append(ids, td.ID)
		}
		if err != nil || len(ids) != len(c.ids) || ids[0] != c.ids[0] {
			t.Errorf("listed with %v: %s, %v", c.args, out, err)
		}
	}
	reopened, _ := todo(agent.UpdateTodoTool, map[string]any{"id": notes.ID, "status": "handed"})
	if reopened.Done != "" {
		t.Errorf("reopened %+v", reopened)
	}
	if _, err := todo(agent.UpdateTodoTool, map[string]any{"id": 99, "status": "done"}); err == nil {
		t.Error("a todo that is not there was updated")
	}
	if _, err := todo(agent.UpdateTodoTool, map[string]any{"id": notes.ID, "status": "later"}); err == nil {
		t.Error("a todo took an unknown status")
	}
}

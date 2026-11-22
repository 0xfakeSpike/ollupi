package hub

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
)

func TestTheHubNamesTheSameTodoStatusesAsTheApp(t *testing.T) {
	for _, st := range account.TodoStatuses {
		if !slices.Contains(TodoStatuses, TodoStatus(st)) {
			t.Errorf("the app knows %q, the hub does not", st)
		}
	}
	if len(TodoStatuses) != len(account.TodoStatuses) {
		t.Errorf("the hub has %v, the app %v", TodoStatuses, account.TodoStatuses)
	}
}

func TestTheHubKeepsTheUsersTodos(t *testing.T) {
	f := setup(t)
	home, docs := f.workspace("home"), f.workspace("docs")
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace); err != nil {
		t.Fatal(err)
	}
	hub, err := f.a.StartThread(f.ctx, "u", home.ID, "", "plan")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.call(hub.ID, agent.AddTodoTool, map[string]any{"title": "release notes", "notes": "after the freeze"})
	if err != nil {
		t.Fatal(err)
	}
	var added Todo
	json.Unmarshal(out, &added)
	if added.ID != 1 || added.Status != TodoStatus(account.TodoOpen) || added.Notes != "after the freeze" {
		t.Fatalf("added %s", out)
	}
	out, err = f.call(hub.ID, agent.ListTodosTool, map[string]any{})
	if err != nil || !strings.Contains(string(out), "release notes") {
		t.Errorf("listed %s, %v", out, err)
	}
	out, err = f.call(hub.ID, agent.StartConversationTool, map[string]any{
		"workspace": docs.ID, "title": "notes", "message": "write the release notes", "todo": added.ID})
	if err != nil {
		t.Fatal(err)
	}
	var started Started
	json.Unmarshal(out, &started)
	if started.Todo == nil || started.Todo.Status != TodoStatus(account.TodoHanded) || started.Todo.Conversation != started.Conversation {
		t.Fatalf("started %s", out)
	}
	if _, err := f.call(hub.ID, agent.StartConversationTool, map[string]any{
		"workspace": docs.ID, "title": "x", "message": "x", "todo": 99}); err == nil {
		t.Error("a conversation started for a todo that does not exist")
	}
	out, err = f.call(hub.ID, agent.UpdateTodoTool, map[string]any{"id": added.ID, "status": "done", "result": "shipped"})
	if err != nil || !strings.Contains(string(out), `"result":"shipped"`) || !strings.Contains(string(out), `"status":"done"`) {
		t.Fatalf("finished %s, %v", out, err)
	}
	out, err = f.call(hub.ID, agent.ListTodosTool, map[string]any{})
	if err != nil || strings.Contains(string(out), "release notes") {
		t.Errorf("a finished todo is listed by default: %s, %v", out, err)
	}
	out, err = f.call(hub.ID, agent.ListTodosTool, map[string]any{"statuses": []any{"done"}})
	if err != nil || !strings.Contains(string(out), "release notes") {
		t.Errorf("the finished todo %s, %v", out, err)
	}
}

package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func ref[T any](v T) *T { return &v }

func TestATodoGoesThroughItsStatuses(t *testing.T) {
	f := setup(t)
	todo := must[account.Todo](t)(f.a.AddTodo(f.ctx, "u", "  write\nthe notes ", "from the release"))
	if todo.ID != 1 || todo.Status != account.TodoOpen || todo.Title != "write the notes" {
		t.Fatalf("added %+v", todo)
	}
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", "a chat"))
	linked := must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{
		Status: ref(account.TodoHanded), Thread: &th.ID}))
	if linked.Status != account.TodoHanded || linked.Thread != th.ID || linked.Workspace != th.Workspace || linked.Ended != nil {
		t.Errorf("handed %+v", linked)
	}
	finished := must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{
		Status: ref(account.TodoDone), Result: ref("notes shipped")}))
	if finished.Result != "notes shipped" || finished.Ended == nil {
		t.Errorf("done %+v", finished)
	}
	kept := must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{Status: ref(account.TodoDropped)}))
	if kept.Ended == nil || !kept.Ended.Equal(*finished.Ended) || kept.Result != "notes shipped" {
		t.Errorf("dropped after done %+v, ended %v", kept, finished.Ended)
	}
	reopened := must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{Status: ref(account.TodoOpen)}))
	if reopened.Ended != nil {
		t.Errorf("reopened %+v", reopened)
	}
	cleared := must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{Result: ref("")}))
	if cleared.Result != "" {
		t.Errorf("the result came back: %+v", cleared)
	}

	if _, err := f.a.UpdateTodo(f.ctx, "u", 99, TodoChange{}); !errors.Is(err, ErrInvalidTodo) || !strings.Contains(err.Error(), "write the notes") {
		t.Errorf("an unknown id: %v", err)
	}
	if _, err := f.a.AddTodo(f.ctx, "u", "  ", ""); !errors.Is(err, ErrInvalidTodo) {
		t.Errorf("an empty title: %v", err)
	}
	if _, err := f.a.AddTodo(f.ctx, "u", strings.Repeat("x", MaxTodoTitleRunes+1), ""); !errors.Is(err, ErrInvalidTodo) {
		t.Errorf("a long title: %v", err)
	}
	if _, err := f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{Thread: ref("no-such-conversation")}); !errors.Is(err, ErrInvalidTodo) {
		t.Errorf("a conversation that does not exist: %v", err)
	}

	must[account.Todo](t)(f.a.UpdateTodo(f.ctx, "u", todo.ID, TodoChange{
		Status: ref(account.TodoHanded), Thread: &th.ID}))
	if err := f.a.DeleteThread(f.ctx, "u", th.ID); err != nil {
		t.Fatal(err)
	}
	back := must[account.Todo](t)(f.a.Todo(f.ctx, "u", todo.ID))
	if back.Status != account.TodoOpen || back.Thread != "" || back.Workspace != "" {
		t.Errorf("a todo whose conversation was deleted: %+v", back)
	}
}

func TestTheOldTodosAreImportedOnce(t *testing.T) {
	f := setup(t)
	root := t.TempDir()
	hub := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", root, "hub"))
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "u", hub.ID, account.HubWorkspace))
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("TODO.md", strings.Join([]string{
		"| 编号 | 事情 | 项目 | 对话 | 状态 | 备忘 |",
		"| --- | --- | --- | --- | --- | --- |",
		"| T1 | fix the build | docs | — | 未开始 | waiting on CI |",
		"| T2 | ship it | — | — | 已派 | — |",
		"",
		"- [ ] old line · ollupi · waiting",
	}, "\n"))
	write("done/2026-01.md", "- [x] T0 earlier · result: landed\n- [x] ~~T9 dropped~~ · 原因：not needed\n")

	todos, _, err := f.a.Todos(f.ctx, "u", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 5 {
		t.Fatalf("imported %+v", todos)
	}
	find := func(title string) account.Todo {
		t.Helper()
		for _, td := range todos {
			if strings.HasPrefix(td.Title, title) {
				return td
			}
		}
		t.Fatalf("no todo %q in %+v", title, todos)
		return account.Todo{}
	}
	if td := find("T1"); td.Status != account.TodoOpen || !strings.Contains(td.Notes, "waiting on CI") || !strings.Contains(td.Notes, "项目：docs") {
		t.Errorf("a table row %+v", td)
	}
	if td := find("T2"); td.Status != account.TodoOpen {
		t.Errorf("a handed todo with no conversation stays open: %+v", td)
	}
	if td := find("old line"); !strings.Contains(td.Notes, "项目：ollupi") {
		t.Errorf("a checklist line %+v", td)
	}
	if td := find("T0 earlier"); td.Status != account.TodoDone || td.Result != "landed" || td.Ended == nil || td.Ended.Month() != time.January {
		t.Errorf("a done todo %+v", td)
	}
	if td := find("T9 dropped"); td.Status != account.TodoDropped || !strings.Contains(td.Notes, "not needed") {
		t.Errorf("a dropped todo %+v", td)
	}
	if _, err := os.Stat(filepath.Join(root, "TODO.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("TODO.md is still there: %v", err)
	}
	if archived, _ := filepath.Glob(filepath.Join(root, "TODO.md.imported-*")); len(archived) != 1 {
		t.Errorf("the archived file %v", archived)
	}
	again, _, err := f.a.Todos(f.ctx, "u", nil, 0)
	if err != nil || len(again) != 5 {
		t.Errorf("read again: %d, %v", len(again), err)
	}
}

func TestAnOldFileDoesNotOverwriteTodaysTodos(t *testing.T) {
	f := setup(t)
	if err := f.a.EnsureUser(f.ctx, "v"); err != nil {
		t.Fatal(err)
	}
	importedTodos.Delete("v")
	root := t.TempDir()
	hub := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "v", root, "hub"))
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "v", hub.ID, account.HubWorkspace))
	if err := os.WriteFile(filepath.Join(root, "TODO.md"), []byte("- [ ] old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must[account.Todo](t)(f.a.AddTodo(f.ctx, "v", "a todo of today", ""))
	todos, _, err := f.a.Todos(f.ctx, "v", nil, 0)
	if err != nil || len(todos) != 1 || todos[0].Title != "a todo of today" {
		t.Fatalf("read %+v, %v", todos, err)
	}
	if _, err := os.Stat(filepath.Join(root, "TODO.md")); err != nil {
		t.Errorf("the old file of a user with todos of their own: %v", err)
	}
}

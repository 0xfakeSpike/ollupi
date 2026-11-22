package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
)

const (
	// todoFile and todoDoneFolder held the hub's todos before the todos table existed.
	todoFile       = "TODO.md"
	todoDoneFolder = "done"
	// todoFileBytes bounds how much of an old todo file the import reads.
	todoFileBytes int64 = 1 << 20
	// todoImportTimeout bounds reading the old files through the sandbox.
	todoImportTimeout = time.Minute
)

// importedTodos records the users whose old todo files this process imported already: each user is read once,
// and an import that fails is tried again on the next call.
var importedTodos sync.Map

// importTodos moves the hub's old TODO.md and done/*.md into the todos table, once per user and process.
// The table wins once it holds a todo of its own; the files are only a source for what was there before.
func (a *App) importTodos(ctx context.Context, user string) error {
	if _, seen := importedTodos.Load(user); seen {
		return nil
	}
	if err := a.importTodoFiles(ctx, user); err != nil {
		return err
	}
	importedTodos.Store(user, struct{}{})
	return nil
}

func (a *App) importTodoFiles(ctx context.Context, user string) error {
	ctx, cancel := context.WithTimeout(ctx, todoImportTimeout)
	defer cancel()
	hub, err := a.hubWorkspace(ctx, user)
	if err != nil {
		return err
	}
	if hub.ID == "" {
		return nil
	}
	box, err := a.boxes(user)
	if err != nil {
		return fmt.Errorf("the sandbox of the user: %w", err)
	}
	todos, found, err := readTodoFiles(ctx, box, hub.Root)
	if err != nil {
		return fmt.Errorf("import the old todos: %w", err)
	}
	imported, err := a.insertImportedTodos(ctx, user, todos)
	if err != nil {
		return fmt.Errorf("import the old todos: %w", err)
	}
	if !found || !imported {
		return nil
	}
	// The old file is archived, not deleted, so nothing it held is lost.
	from := filepath.Join(hub.Root, todoFile)
	to := fmt.Sprintf("%s.imported-%s", from, time.Now().Format("2006-01-02"))
	if err := box.Move(ctx, from, to); err != nil {
		return fmt.Errorf("the old todos were imported, but %s could not be renamed: %w", todoFile, err)
	}
	return nil
}

// hubWorkspace is the user's hub workspace while it is not archived.
func (a *App) hubWorkspace(ctx context.Context, user string) (account.Workspace, error) {
	var hub account.Workspace
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		ws, err := r.Workspaces(user)
		if err != nil {
			return err
		}
		for _, w := range ws {
			if w.Kind == account.HubWorkspace && w.Archived == nil {
				hub = w
				break
			}
		}
		return nil
	})
	return hub, err
}

// readTodoFiles reads the hub's old todo files: TODO.md at its root, and the month files in done/.
// A todo that ended in a month file ended on its first day; one TODO.md still lists ended at the import.
func readTodoFiles(ctx context.Context, box sandbox.Box, root string) (todos []oldTodo, found bool, err error) {
	text, err := readTodoFile(ctx, box, filepath.Join(root, todoFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, false, err
	default:
		found = true
		now := time.Now().UTC()
		todos = append(todos, parseTodoFile(string(text), &now)...)
	}
	entries, err := box.ReadDir(ctx, filepath.Join(root, todoDoneFolder))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return todos, found, nil
	case err != nil:
		return nil, false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		month, err := time.Parse("2006-01.md", e.Name())
		if err != nil {
			continue
		}
		text, err := readTodoFile(ctx, box, filepath.Join(root, todoDoneFolder, e.Name()))
		if err != nil {
			return nil, false, fmt.Errorf("%s/%s: %w", todoDoneFolder, e.Name(), err)
		}
		todos = append(todos, parseTodoFile(string(text), &month)...)
	}
	return todos, found, nil
}

func readTodoFile(ctx context.Context, box sandbox.Box, path string) ([]byte, error) {
	info, err := box.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	return sandbox.ReadFile(ctx, box, path, todoFileBytes)
}

// insertImportedTodos stores the imported todos in one transaction, unless the table holds a todo already:
// then the user writes todos through the tools, and the old files are not read into it.
func (a *App) insertImportedTodos(ctx context.Context, user string, todos []oldTodo) (imported bool, err error) {
	now := time.Now().UTC()
	err = a.st.Update(ctx, func(tx store.Tx) error {
		existing, _, err := tx.Todos(user, store.TodoFilter{Limit: 1})
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		imported = true
		for _, old := range todos {
			t := old.stored()
			if t.Thread != "" {
				th, err := thread(tx, user, t.Thread)
				switch {
				case errors.Is(err, store.ErrNotFound):
					t.Thread = ""
				case err != nil:
					return err
				default:
					t.Workspace = th.Workspace
				}
			}
			if t.Thread == "" && t.Status == account.TodoHanded {
				t.Status = account.TodoOpen
			}
			t.Created, t.Updated = now, now
			if t.Ended != nil {
				t.Created = *t.Ended
			}
			if _, err := tx.AddTodo(user, t); err != nil {
				return err
			}
		}
		return nil
	})
	return imported, err
}

// oldTodo is a todo read from one of the hub's old files, before it is stored.
type oldTodo struct {
	title   string
	status  account.TodoStatus
	project string
	thread  string
	notes   string
	result  string
	ended   *time.Time
}

// stored is the todo the table keeps for it. The old project is a name the table does not link by, so it is
// kept in the notes; a conversation link is checked against the store when the todo is inserted.
func (o oldTodo) stored() account.Todo {
	notes := o.notes
	if o.project != "" {
		notes = joinNote(notes, "项目："+o.project)
	}
	return account.Todo{Title: o.title, Thread: o.thread, Status: o.status, Notes: notes, Result: o.result, Ended: o.ended}
}

var (
	// todoTaskLine is a checklist line of the old files: - [ ] ... or - [x] ...
	todoTaskLine = regexp.MustCompile(`^[-*+] \[( |x|X)\]\s+(.*)$`)
	// todoThreadLink is how an old todo linked the conversation working on it: [title](#thread-<id>).
	todoThreadLink = regexp.MustCompile(`^\[[^\]]*\]\(#thread-([^)\s]+)\)$`)
	// todoStruck is a dropped todo's title in a done file: ~~...~~.
	todoStruck = regexp.MustCompile(`^~~(.+)~~$`)
)

// parseTodoFile reads the todos of an old hub file: the rows of a table with its columns
// (编号, 事情, 项目, 对话, 状态, 备忘, 结果), and checklist lines with what they carry.
// Lines it cannot read are skipped, not guessed at; ended dates todos that ended when the file does not say.
func parseTodoFile(text string, ended *time.Time) []oldTodo {
	var todos []oldTodo
	var columns []string
	last := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			last = -1
			continue
		}
		if cells, ok := tableCells(trimmed); ok {
			last = -1
			switch {
			case slices.Contains(cells, "事情"):
				columns = cells
			case tableRule(cells):
			default:
				if t, ok := tableTodo(columns, cells); ok {
					todos = append(todos, t)
					last = len(todos) - 1
				}
			}
			continue
		}
		columns = nil
		if m := todoTaskLine.FindStringSubmatch(trimmed); m != nil {
			todos = append(todos, taskTodo(m[1] != " ", m[2]))
			last = len(todos) - 1
			continue
		}
		if last >= 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			t := &todos[last]
			t.notes = joinNote(t.notes, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			continue
		}
		last = -1
	}
	for i := range todos {
		t := &todos[i]
		if (t.status == account.TodoDone || t.status == account.TodoDropped) && t.ended == nil {
			t.ended = ended
		}
		t.title = clampRunes(oneLine(t.title), MaxTodoTitleRunes)
		t.notes = clampRunes(t.notes, MaxTodoNotesRunes)
		t.result = clampRunes(oneLine(t.result), MaxTodoResultRunes)
	}
	return todos
}

// tableCells splits a row of a Markdown table, or reports that the line is no row.
func tableCells(line string) ([]string, bool) {
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") || len(line) < 2 {
		return nil, false
	}
	cells := strings.Split(strings.Trim(line, "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells, true
}

// tableRule reports whether a row only draws the line under a table's columns.
func tableRule(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// tableTodo reads one row of an old table, by the columns the table declared.
func tableTodo(columns, cells []string) (oldTodo, bool) {
	get := func(column string) string {
		if i := slices.Index(columns, column); i >= 0 && i < len(cells) {
			return unset(cells[i])
		}
		return ""
	}
	task := get("事情")
	if task == "" {
		return oldTodo{}, false
	}
	t := oldTodo{title: task, status: readTodoStatus(get("状态")), result: get("结果")}
	if id := get("编号"); id != "" {
		t.title = id + " " + task
	}
	if cell := get("对话"); cell != "" {
		if m := todoThreadLink.FindStringSubmatch(cell); m != nil {
			t.thread = m[1]
		} else {
			t.notes = joinNote(t.notes, "对话："+cell)
		}
	}
	t.project = get("项目")
	t.notes = joinNote(t.notes, get("备忘"))
	return t, true
}

// taskTodo reads one checklist line of an old file: title · project · 结果：... , the title struck through
// with ~~...~~ when the task was dropped.
func taskTodo(checked bool, body string) oldTodo {
	parts := strings.Split(body, " · ")
	t := oldTodo{title: strings.TrimSpace(parts[0])}
	switch {
	case todoStruck.MatchString(t.title):
		t.title = todoStruck.FindStringSubmatch(t.title)[1]
		t.status = account.TodoDropped
	case checked:
		t.status = account.TodoDone
	default:
		t.status = account.TodoOpen
	}
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case todoThreadLink.MatchString(part):
			t.thread = todoThreadLink.FindStringSubmatch(part)[1]
		default:
			if label, value, ok := cutLabel(part); ok {
				switch label {
				case "结果", "result", "outcome":
					t.result = value
				default:
					t.notes = joinNote(t.notes, value)
				}
				continue
			}
			if t.project == "" && !strings.ContainsAny(part, ":：") {
				t.project = part
				continue
			}
			t.notes = joinNote(t.notes, part)
		}
	}
	return t
}

// cutLabel splits "结果：..." into its label and what follows it.
func cutLabel(part string) (label, value string, ok bool) {
	for _, sep := range []string{"：", ":"} {
		if l, v, found := strings.Cut(part, sep); found {
			return strings.ToLower(strings.TrimSpace(l)), strings.TrimSpace(v), true
		}
	}
	return "", "", false
}

// unset reads a table cell that says nothing as empty: the old file wrote — for "none".
func unset(cell string) string {
	cell = strings.TrimSpace(cell)
	if cell == "—" || cell == "-" || cell == "–" {
		return ""
	}
	return cell
}

func readTodoStatus(s string) account.TodoStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(account.TodoHanded), "已派", "已交给会话":
		return account.TodoHanded
	case string(account.TodoDone), "完成":
		return account.TodoDone
	case string(account.TodoDropped), "放弃", "终止":
		return account.TodoDropped
	default:
		return account.TodoOpen
	}
}

func joinNote(notes, add string) string {
	switch {
	case add == "":
		return notes
	case notes == "":
		return add
	default:
		return notes + "\n" + add
	}
}

func clampRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

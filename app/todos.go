package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

const (
	// MaxTodoTitleRunes is how long a todo's title may be.
	MaxTodoTitleRunes = 200
	// MaxTodoNotesRunes is how long a todo's notes may be.
	MaxTodoNotesRunes = 2000
	// MaxTodoResultRunes is how long the result of a finished todo may be.
	MaxTodoResultRunes = 500
	MaxTodoLevels      = 3
)

// TodoChange is what UpdateTodo changes in a todo. A field left nil stays as it is; a pointer to "" clears it.
type TodoChange struct {
	Parent *int64
	Title  *string
	Status *account.TodoStatus
	Notes  *string
	Result *string
	// Thread links the todo to one of the user's conversations, and to the workspace that conversation is in;
	// a pointer to "" unlinks it.
	Thread *string
}

// Todos lists the user's todos in the given statuses, in the order the store keeps them: the unfinished ones
// first, then the finished ones by when they ended; with no statuses, all of them. Limit 0 reads all of them.
func (a *App) Todos(ctx context.Context, user string, statuses []account.TodoStatus, limit int) (todos []account.Todo, more bool, seq int64, err error) {
	for _, st := range statuses {
		if err := todoStatus(st); err != nil {
			return nil, false, 0, err
		}
	}
	if err := a.importTodos(ctx, user); err != nil {
		return nil, false, 0, err
	}
	err = a.st.Read(ctx, func(r store.Reader) error {
		if todos, more, err = r.Todos(user, store.TodoFilter{Statuses: statuses, Limit: limit}); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return todos, more, seq, err
}

// Todo reads one of the user's todos, naming the ones that exist when the id is unknown.
func (a *App) Todo(ctx context.Context, user string, id int64) (account.Todo, error) {
	var t account.Todo
	err := a.st.Read(ctx, func(r store.Reader) (err error) { t, err = todo(r, user, id); return err })
	return t, err
}

// AddTodo records an open todo of the user's, under parent unless it is 0, and gives it the user's next todo id.
func (a *App) AddTodo(ctx context.Context, user, title, notes string, parent int64) (account.Todo, error) {
	var err error
	if title, err = todoTitle(title); err != nil {
		return account.Todo{}, err
	}
	if title == "" {
		return account.Todo{}, fmt.Errorf("%w: a todo needs a title", ErrInvalidTodo)
	}
	if notes, err = todoNotes(notes); err != nil {
		return account.Todo{}, err
	}
	now := time.Now().UTC()
	var added account.Todo
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		if err := todoParent(tx, user, 0, parent); err != nil {
			return err
		}
		added, err = tx.AddTodo(user, account.Todo{Parent: parent, Title: title, Status: account.TodoOpen, Notes: notes, Created: now, Updated: now})
		return err
	})
	return added, err
}

// UpdateTodo applies a change to one of the user's todos. Entering done or dropped records when; leaving them
// forgets it. Linking a conversation does not change the status.
func (a *App) UpdateTodo(ctx context.Context, user string, id int64, c TodoChange) (account.Todo, error) {
	if c.Title != nil {
		title, err := todoTitle(*c.Title)
		if err != nil {
			return account.Todo{}, err
		}
		if title == "" {
			return account.Todo{}, fmt.Errorf("%w: a todo needs a title", ErrInvalidTodo)
		}
		c.Title = &title
	}
	if c.Status != nil {
		if err := todoStatus(*c.Status); err != nil {
			return account.Todo{}, err
		}
	}
	if c.Notes != nil {
		notes, err := todoNotes(*c.Notes)
		if err != nil {
			return account.Todo{}, err
		}
		c.Notes = &notes
	}
	if c.Result != nil {
		result, err := todoResult(*c.Result)
		if err != nil {
			return account.Todo{}, err
		}
		c.Result = &result
	}
	return edit(ctx, a.st,
		func(tx store.Tx) (account.Todo, error) { return todo(tx, user, id) },
		func(tx store.Tx, t *account.Todo) error {
			if c.Parent != nil {
				if err := todoParent(tx, user, t.ID, *c.Parent); err != nil {
					return err
				}
				t.Parent = *c.Parent
			}
			if c.Thread != nil {
				if *c.Thread == "" {
					t.Thread, t.Workspace = "", ""
				} else {
					th, err := thread(tx, user, *c.Thread)
					if errors.Is(err, store.ErrNotFound) {
						return fmt.Errorf("%w: the user has no conversation %q", ErrInvalidTodo, *c.Thread)
					}
					if err != nil {
						return err
					}
					t.Thread, t.Workspace = th.ID, th.Workspace
				}
			}
			if c.Title != nil {
				t.Title = *c.Title
			}
			wasEnded := t.Ended != nil
			if c.Status != nil {
				t.Status = *c.Status
			}
			if c.Notes != nil {
				t.Notes = *c.Notes
			}
			if c.Result != nil {
				t.Result = *c.Result
			}
			t.Updated = time.Now().UTC()
			switch ended := t.Status == account.TodoDone || t.Status == account.TodoDropped; {
			case ended && !wasEnded:
				at := t.Updated
				t.Ended = &at
			case !ended:
				t.Ended = nil
			}
			return nil
		},
		func(tx store.Tx, t account.Todo) error { return tx.PutTodo(user, t) })
}

// todo reads one of the user's todos, naming the ones that exist when the id is unknown, so a caller can correct it.
func todo(r store.Reader, user string, id int64) (account.Todo, error) {
	t, err := r.Todo(user, id)
	if !errors.Is(err, store.ErrNotFound) {
		return t, err
	}
	known, _, lerr := r.Todos(user, store.TodoFilter{Limit: 20})
	if lerr != nil {
		return account.Todo{}, lerr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the user has no todo %d", id)
	if len(known) == 0 {
		b.WriteString("; there are no todos")
		return account.Todo{}, fmt.Errorf("%w: %s", ErrInvalidTodo, b.String())
	}
	b.WriteString("; the todos are:")
	for _, k := range known {
		fmt.Fprintf(&b, "\n%d %s", k.ID, k.Title)
	}
	return account.Todo{}, fmt.Errorf("%w: %s", ErrInvalidTodo, b.String())
}

func todoParent(r store.Reader, user string, id, parent int64) error {
	above := 0
	for at := parent; at != 0; above++ {
		if at == id {
			return fmt.Errorf("%w: todo %d cannot be under %d, which is %d itself or one of its parts", ErrInvalidTodo, id, parent, id)
		}
		p, err := todo(r, user, at)
		if err != nil {
			return err
		}
		at = p.Parent
	}
	if above == 0 {
		return nil
	}
	levels := 1
	if id != 0 {
		all, _, err := r.Todos(user, store.TodoFilter{})
		if err != nil {
			return err
		}
		levels = todoLevels(all, id)
	}
	if above+levels > MaxTodoLevels {
		return fmt.Errorf("%w: todos go at most %d levels deep, and under todo %d this one would make %d", ErrInvalidTodo, MaxTodoLevels, parent, above+levels)
	}
	return nil
}

func todoLevels(all []account.Todo, id int64) int {
	deepest := 0
	for _, t := range all {
		if t.Parent == id {
			deepest = max(deepest, todoLevels(all, t.ID))
		}
	}
	return deepest + 1
}

func todoTitle(title string) (string, error) {
	title = oneLine(title)
	if len([]rune(title)) > MaxTodoTitleRunes {
		return "", fmt.Errorf("%w: a todo's title is at most %d characters", ErrInvalidTodo, MaxTodoTitleRunes)
	}
	return title, nil
}

func todoNotes(notes string) (string, error) {
	notes = strings.TrimSpace(notes)
	if len([]rune(notes)) > MaxTodoNotesRunes {
		return "", fmt.Errorf("%w: a todo's notes are at most %d characters", ErrInvalidTodo, MaxTodoNotesRunes)
	}
	return notes, nil
}

func todoResult(result string) (string, error) {
	result = oneLine(result)
	if len([]rune(result)) > MaxTodoResultRunes {
		return "", fmt.Errorf("%w: a todo's result is at most %d characters", ErrInvalidTodo, MaxTodoResultRunes)
	}
	return result, nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func todoStatus(st account.TodoStatus) error {
	if !slices.Contains(account.TodoStatuses, st) {
		return fmt.Errorf("%w: a todo is one of %q, not %q", ErrInvalidTodo, account.TodoStatuses, st)
	}
	return nil
}

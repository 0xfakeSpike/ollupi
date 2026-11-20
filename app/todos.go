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

// MaxTodoTitleRunes is how long a todo's title may be.
const MaxTodoTitleRunes = 200

// TodoChange is what UpdateTodo changes in a todo; a field left empty stays as it is.
type TodoChange struct {
	Title  string
	Status account.TodoStatus
	Notes  string
	Result string
	// Thread links the todo to one of the user's conversations, and to the workspace that conversation is in.
	Thread string
}

// Todos lists the user's todos in the given statuses, in the order they were added; with no statuses, all of them.
func (a *App) Todos(ctx context.Context, user string, statuses []account.TodoStatus) ([]account.Todo, error) {
	for _, st := range statuses {
		if err := todoStatus(st); err != nil {
			return nil, err
		}
	}
	if len(statuses) == 0 {
		statuses = account.TodoStatuses
	}
	var out []account.Todo
	err := a.st.Read(ctx, func(r store.Reader) (err error) { out, err = r.Todos(user, statuses); return err })
	return out, err
}

// AddTodo records an open todo of the user's and gives it the user's next todo id.
func (a *App) AddTodo(ctx context.Context, user, title, notes string) (account.Todo, error) {
	title, err := todoTitle(title)
	if err != nil {
		return account.Todo{}, err
	}
	if title == "" {
		return account.Todo{}, fmt.Errorf("%w: a todo needs a title", ErrInvalidTodo)
	}
	now := time.Now().UTC()
	var added account.Todo
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		added, err = tx.AddTodo(user, account.Todo{Title: title, Status: account.TodoOpen, Notes: notes, Created: now, Updated: now})
		return err
	})
	return added, err
}

// UpdateTodo applies a change to one of the user's todos. Marking it done records when; leaving done forgets it.
func (a *App) UpdateTodo(ctx context.Context, user string, id int64, c TodoChange) (account.Todo, error) {
	title, err := todoTitle(c.Title)
	if err != nil {
		return account.Todo{}, err
	}
	if c.Status != "" {
		if err := todoStatus(c.Status); err != nil {
			return account.Todo{}, err
		}
	}
	return edit(ctx, a.st,
		func(tx store.Tx) (account.Todo, error) {
			t, err := tx.Todo(user, id)
			if errors.Is(err, store.ErrNotFound) {
				return account.Todo{}, fmt.Errorf("the user has no todo %d: %w", id, store.ErrNotFound)
			}
			return t, err
		},
		func(tx store.Tx, t *account.Todo) error {
			if c.Thread != "" {
				th, err := thread(tx, user, c.Thread)
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("the user has no conversation %q: %w", c.Thread, store.ErrNotFound)
				}
				if err != nil {
					return err
				}
				t.Workspace, t.Thread = th.Workspace, th.ID
			}
			if title != "" {
				t.Title = title
			}
			wasDone := t.Status == account.TodoDone
			if c.Status != "" {
				t.Status = c.Status
			}
			if c.Notes != "" {
				t.Notes = c.Notes
			}
			if c.Result != "" {
				t.Result = c.Result
			}
			t.Updated = time.Now().UTC()
			switch done := t.Status == account.TodoDone; {
			case done && !wasDone:
				at := t.Updated
				t.Done = &at
			case !done:
				t.Done = nil
			}
			return nil
		},
		func(tx store.Tx, t account.Todo) error { return tx.PutTodo(user, t) })
}

func todoTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > MaxTodoTitleRunes {
		return "", fmt.Errorf("%w: a todo's title is at most %d characters", ErrInvalidTodo, MaxTodoTitleRunes)
	}
	return title, nil
}

func todoStatus(st account.TodoStatus) error {
	if !slices.Contains(account.TodoStatuses, st) {
		return fmt.Errorf("%w: a todo is one of %q, not %q", ErrInvalidTodo, account.TodoStatuses, st)
	}
	return nil
}

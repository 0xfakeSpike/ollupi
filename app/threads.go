package app

import (
	"context"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

func (a *App) StartThread(ctx context.Context, user, workspace, environment, title string) (store.Thread, error) {
	title, err := account.Title(title, false)
	if err != nil {
		return store.Thread{}, err
	}
	var th store.Thread
	err = a.st.Update(ctx, func(tx store.Tx) (err error) {
		th, err = newThread(tx, user, workspace, environment, title)
		return err
	})
	return th, err
}

func newThread(tx store.Tx, user, workspace, environment, title string) (store.Thread, error) {
	if workspace != "" {
		w, err := tx.Workspace(user, workspace)
		if err != nil {
			return store.Thread{}, err
		}
		if err := w.Usable(); err != nil {
			return store.Thread{}, err
		}
	}
	envs, err := tx.Environments(user)
	if err != nil {
		return store.Thread{}, err
	}
	env := envs[0].ID
	if environment != "" {
		e, err := tx.Environment(user, environment)
		if err != nil {
			return store.Thread{}, err
		}
		env = e.ID
	}
	now := time.Now().UTC()
	th := store.Thread{User: user, Thread: account.Thread{ID: uuid.NewString(), Title: title, Workspace: workspace, Environment: env, Created: now, Updated: now}}
	if err := tx.CreateThread(th); err != nil {
		return store.Thread{}, err
	}
	return tx.Thread(th.ID)
}

type HandOff struct {
	Workspace string
	Title     string
	Message   string
	From      string
	Todo      int64
}

type HandedOff struct {
	Thread     store.Thread
	Turn       conversation.Turn
	InProgress bool
	Todo       *account.Todo
}

func (a *App) HandOff(ctx context.Context, user string, h HandOff) (HandedOff, error) {
	title, err := account.Title(h.Title, false)
	if err != nil {
		return HandedOff{}, err
	}
	var out HandedOff
	err = a.st.Update(ctx, func(tx store.Tx) error {
		var t account.Todo
		if h.Todo != 0 {
			if t, err = todo(tx, user, h.Todo); err != nil {
				return err
			}
		}
		from, err := sender(tx, user, h.From)
		if err != nil {
			return err
		}
		th, err := newThread(tx, user, h.Workspace, "", title)
		if err != nil {
			return err
		}
		turn, inProgress, err := a.queue(tx, th, conversation.UserMessage{Text: h.Message, From: from}, "", "")
		if err != nil {
			return err
		}
		out = HandedOff{Thread: th, Turn: turn, InProgress: inProgress}
		if h.Todo == 0 {
			return nil
		}
		t.Status, t.Thread, t.Workspace, t.Ended = account.TodoHanded, th.ID, th.Workspace, nil
		t.Updated = time.Now().UTC()
		out.Todo = &t
		return tx.PutTodo(user, t)
	})
	return out, err
}

func (a *App) Threads(ctx context.Context, user string, f store.ThreadFilter) (threads []store.Thread, more bool, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		threads, more, err = r.Threads(user, f)
		return err
	})
	return threads, more, err
}

func (a *App) Page(ctx context.Context, user, id string, before int64, limit int) (th store.Thread, page store.Page, more bool, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if th, err = thread(r, user, id); err != nil {
			return err
		}
		if page, more, err = r.Page(id, before, limit); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return th, page, more, seq, err
}

func (a *App) RenameThread(ctx context.Context, user, id, title string) (store.Thread, error) {
	title, err := account.Title(title, true)
	if err != nil {
		return store.Thread{}, err
	}
	return a.changeThread(ctx, user, id, func(_ store.Tx, th *store.Thread) error {
		th.Title = title
		return nil
	})
}

func (a *App) MarkThreadRead(ctx context.Context, user, id string) (store.Thread, error) {
	return a.changeThread(ctx, user, id, func(_ store.Tx, th *store.Thread) error {
		now := time.Now().UTC()
		th.Read = &now
		return nil
	})
}

func (a *App) ArchiveThread(ctx context.Context, user, id string, archived bool) (store.Thread, error) {
	return a.changeThread(ctx, user, id, func(_ store.Tx, th *store.Thread) error {
		th.Archived = nil
		if !archived {
			return nil
		}
		if th.Status != conversation.ThreadIdle {
			return ErrThreadBusy
		}
		now := time.Now().UTC()
		th.Archived = &now
		return nil
	})
}

func (a *App) changeThread(ctx context.Context, user, id string, change func(store.Tx, *store.Thread) error) (store.Thread, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (store.Thread, error) { return thread(tx, user, id) },
		func(tx store.Tx, th *store.Thread) error {
			th.Updated = time.Now().UTC()
			return change(tx, th)
		},
		store.Tx.UpdateThread)
}

func (a *App) DeleteThread(ctx context.Context, user, id string) error {
	gone := []string{id}
	err := a.st.Update(ctx, func(tx store.Tx) error {
		th, err := thread(tx, user, id)
		if err != nil {
			return err
		}
		if th.Status != conversation.ThreadIdle {
			return ErrThreadBusy
		}
		children, err := tx.Children(id)
		if err != nil {
			return err
		}
		for _, c := range children {
			gone = append(gone, c.ID)
		}
		return tx.DeleteThread(id)
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachmentTimeout)
	defer cancel()
	for _, thread := range gone {
		if err := a.attachments.DeletePrefix(ctx, thread+"/"); err != nil {
			return fmt.Errorf("the conversation is deleted, but not its attachments: %w", err)
		}
	}
	return nil
}

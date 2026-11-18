package app

import (
	"context"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/uuid"
)

func (a *App) StartThread(ctx context.Context, u, workspace, environment, title string) (store.Thread, error) {
	title, err := user.Title(title, false)
	if err != nil {
		return store.Thread{}, err
	}
	var th store.Thread
	err = a.st.Update(ctx, func(tx store.Tx) error {
		if workspace != "" {
			w, err := tx.Workspace(u, workspace)
			if err != nil {
				return err
			}
			if err := w.Usable(); err != nil {
				return err
			}
		}
		envs, err := tx.Environments(u)
		if err != nil {
			return err
		}
		env := envs[0].ID
		if environment != "" {
			e, err := tx.Environment(u, environment)
			if err != nil {
				return err
			}
			env = e.ID
		}
		now := time.Now().UTC()
		th = store.Thread{User: u, Thread: user.Thread{ID: uuid.NewString(), Title: title, Workspace: workspace, Environment: env, Created: now, Updated: now}}
		if err := tx.CreateThread(th); err != nil {
			return err
		}
		th, err = tx.Thread(th.ID)
		return err
	})
	return th, err
}

func (a *App) Threads(ctx context.Context, u string, f store.ThreadFilter) (threads []store.Thread, more bool, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		threads, more, err = r.Threads(u, f)
		return err
	})
	return threads, more, err
}

func (a *App) Page(ctx context.Context, u, id string, before int64, limit int) (th store.Thread, page store.Page, more bool, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if th, err = thread(r, u, id); err != nil {
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

func (a *App) RenameThread(ctx context.Context, u, id, title string) (store.Thread, error) {
	title, err := user.Title(title, true)
	if err != nil {
		return store.Thread{}, err
	}
	return a.changeThread(ctx, u, id, func(_ store.Tx, th *store.Thread) error {
		th.Title = title
		return nil
	})
}

func (a *App) MarkThreadRead(ctx context.Context, u, id string) (store.Thread, error) {
	return a.changeThread(ctx, u, id, func(_ store.Tx, th *store.Thread) error {
		now := time.Now().UTC()
		th.Read = &now
		return nil
	})
}

func (a *App) ArchiveThread(ctx context.Context, u, id string, archived bool) (store.Thread, error) {
	return a.changeThread(ctx, u, id, func(_ store.Tx, th *store.Thread) error {
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

func (a *App) changeThread(ctx context.Context, u, id string, change func(store.Tx, *store.Thread) error) (store.Thread, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (store.Thread, error) { return topThread(tx, u, id) },
		func(tx store.Tx, th *store.Thread) error {
			th.Updated = time.Now().UTC()
			return change(tx, th)
		},
		store.Tx.UpdateThread)
}

func (a *App) DeleteThread(ctx context.Context, u, id string) error {
	err := a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, id)
		if err != nil {
			return err
		}
		if th.Status != conversation.ThreadIdle {
			return ErrThreadBusy
		}
		return tx.DeleteThread(id)
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachmentTimeout)
	defer cancel()
	if err := a.attachments.DeletePrefix(ctx, id+"/"); err != nil {
		return fmt.Errorf("the conversation is deleted, but not its attachments: %w", err)
	}
	return nil
}

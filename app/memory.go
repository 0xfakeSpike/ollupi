package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) Memories(ctx context.Context, user string, place memory.Place) ([]memory.Memory, error) {
	if err := place.Check(); err != nil {
		return nil, err
	}
	var ms []memory.Memory
	err := a.st.Read(ctx, func(r store.Reader) (err error) { ms, err = r.Memories(user, place); return err })
	return ms, err
}

type MemoryStatus struct {
	memory.State
	Unread int64
	Active int
}

func (a *App) MemoryStatus(ctx context.Context, user string) (MemoryStatus, error) {
	var s MemoryStatus
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		if s.State, err = r.MemoryState(user); err != nil {
			return err
		}
		if s.Unread, err = r.Unread(user, s.State.Cursor); err != nil {
			return err
		}
		ms, err := r.Memories(user)
		for _, m := range ms {
			if m.Status == memory.Active {
				s.Active++
			}
		}
		return err
	})
	return s, err
}

func (a *App) EditMemory(ctx context.Context, user, id, text string) (memory.Memory, error) {
	return a.changeMemory(ctx, user, id, func(_ store.Reader, m memory.Memory) (memory.Memory, error) {
		return memory.Edit(m, text, time.Now().UTC())
	})
}

func (a *App) MoveMemory(ctx context.Context, user, id string) (memory.Memory, error) {
	return a.changeMemory(ctx, user, id, func(r store.Reader, m memory.Memory) (memory.Memory, error) {
		there, err := r.Memories(user, memory.Destination(m))
		if err != nil {
			return memory.Memory{}, err
		}
		return memory.Move(m, there, time.Now().UTC())
	})
}

func (a *App) ForgetMemory(ctx context.Context, user, id string) error {
	_, err := a.changeMemory(ctx, user, id, func(_ store.Reader, m memory.Memory) (memory.Memory, error) {
		return memory.Forget(m, time.Now().UTC()), nil
	})
	return err
}

func (a *App) changeMemory(ctx context.Context, user, id string, change func(store.Reader, memory.Memory) (memory.Memory, error)) (memory.Memory, error) {
	return edit(ctx, a.st,
		func(tx store.Tx) (memory.Memory, error) { return tx.Memory(user, id) },
		func(tx store.Tx, m *memory.Memory) (err error) {
			*m, err = change(tx, *m)
			return err
		},
		store.Tx.PutMemory)
}

const (
	MaxSearchRunes = 200
	MaxSearchTerms = 8
	searchTimeout  = 5 * time.Second
)

func (a *App) Search(ctx context.Context, user, query, workspace string, limit int) (store.Found, []memory.Memory, error) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return store.Found{}, nil, fmt.Errorf("%w: query is required", ErrInvalidQuery)
	}
	if utf8.RuneCountInString(query) > MaxSearchRunes || len(terms) > MaxSearchTerms {
		return store.Found{}, nil, fmt.Errorf("%w: query must be at most %d characters and %d words", ErrInvalidQuery, MaxSearchRunes, MaxSearchTerms)
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	var found store.Found
	var notes []memory.Memory
	err := a.st.Read(ctx, func(r store.Reader) (err error) {
		if found, err = r.Search(user, terms, workspace, limit); err != nil {
			return err
		}
		var places []memory.Place
		if workspace != "" {
			places = append(places, memory.Local(workspace))
		}
		notes, err = r.Memories(user, places...)
		return err
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return store.Found{}, nil, ErrSearchTimedOut
	}
	return found, memory.Search(notes, query, limit), err
}

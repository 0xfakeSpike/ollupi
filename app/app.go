package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
)

var (
	ErrWorkspaceBusy    = errors.New("a conversation in the workspace has a turn in progress or queued; interrupt it first")
	ErrThreadBusy       = errors.New("the conversation has a turn in progress or queued; interrupt it first")
	ErrThreadArchived   = errors.New("the conversation is archived; restore it before sending to it")
	ErrQueueFull        = fmt.Errorf("the conversation has %d messages waiting already; send this one once they start", maxQueued)
	ErrNoActiveTurn     = errors.New("the conversation has no turn in progress")
	ErrTurnMismatch     = errors.New("the turn in progress is not the one named")
	ErrNoSuchRequest    = errors.New("the conversation does not wait on that request")
	ErrInvalidMessage   = errors.New("invalid message")
	ErrInvalidAnswer    = errors.New("invalid answer")
	ErrInvalidDecision  = errors.New("invalid decision")
	ErrInvalidPath      = errors.New("invalid path")
	ErrEnvironmentInUse = errors.New("a conversation runs in the environment")
	ErrInvalidQuery     = errors.New("invalid query")
	ErrSearchTimedOut   = errors.New("the search took too long; try longer or fewer words")
)

type Models interface {
	Models() []llm.Model
	Limits(m llm.Model) (llm.Limits, error)
}

type Boxes func(user string) (sandbox.Box, error)

type Statuses func(user string) ([]account.MCPStatus, error)

type Attachments interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) error
}

type App struct {
	st          store.Store
	models      Models
	tools       tool.Tools
	statuses    Statuses
	boxes       Boxes
	attachments Attachments
	cal         cron.Calendar
	deployment  policy.Settings

	mu     sync.Mutex
	moving map[string]bool
}

func New(st store.Store, models Models, tools tool.Tools, statuses Statuses, boxes Boxes, attachments Attachments, cal cron.Calendar, deployment policy.Settings) *App {
	return &App{st: st, models: models, tools: tools, statuses: statuses, boxes: boxes, attachments: attachments, cal: cal, deployment: deployment, moving: map[string]bool{}}
}

func (a *App) EnsureUser(ctx context.Context, user string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser(user) })
}

func (a *App) Models() ([]llm.Model, string) {
	models := a.models.Models()
	return models, models[0].Connection
}

func (a *App) Tools(ctx context.Context, user string) ([]tool.Spec, error) {
	set, err := a.tools(ctx, user)
	if err != nil {
		return nil, err
	}
	return set.Specs(), nil
}

func (a *App) pickModel(connection string, last *llm.Model) (llm.Model, error) {
	models := a.models.Models()
	if connection == "" && last != nil {
		connection = last.Connection
	}
	if connection == "" {
		connection = models[0].Connection
	}
	i := slices.IndexFunc(models, func(m llm.Model) bool { return m.Connection == connection })
	if i < 0 {
		return llm.Model{}, fmt.Errorf("%w: connection %q is unavailable; restore it in Settings or select another model", ErrInvalidMessage, connection)
	}
	return models[i].Clone(), nil
}

func thread(r store.Reader, user, id string) (store.Thread, error) {
	th, err := r.Thread(id)
	if err != nil {
		return store.Thread{}, err
	}
	if th.User != user {
		return store.Thread{}, fmt.Errorf("thread %s: %w", id, store.ErrNotFound)
	}
	return th, nil
}

func edit[T any](ctx context.Context, st store.Store, get func(store.Tx) (T, error), change func(store.Tx, *T) error, put func(store.Tx, T) error) (T, error) {
	var v T
	err := st.Update(ctx, func(tx store.Tx) (err error) {
		if v, err = get(tx); err != nil {
			return err
		}
		if err := change(tx, &v); err != nil {
			return err
		}
		return put(tx, v)
	})
	return v, err
}

func view(r store.Reader, user, id string) (conversation.View, error) {
	if _, err := thread(r, user, id); err != nil {
		return conversation.View{}, err
	}
	return r.View(id)
}

func topThread(r store.Reader, user, id string) (store.Thread, error) {
	th, err := thread(r, user, id)
	if err != nil {
		return store.Thread{}, err
	}
	if th.Parent != "" {
		return store.Thread{}, account.ErrSubagentThread
	}
	return th, nil
}

func (a *App) toolbox(ctx context.Context, user string) (policy.Toolbox, error) {
	set, err := a.tools(ctx, user)
	if err != nil {
		return policy.Toolbox{}, fmt.Errorf("the tools of the user: %w", err)
	}
	return tool.Toolbox(set), nil
}

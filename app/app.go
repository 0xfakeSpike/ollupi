package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/environment"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
)

var (
	ErrNoHub              = errors.New("this deployment offers no hub")
	ErrSecondHub          = errors.New("the user has a hub workspace already; make it a project workspace first")
	ErrWorkspaceBusy      = errors.New("a conversation in the workspace has a turn in progress or queued; interrupt it first")
	ErrThreadBusy         = errors.New("the conversation has a turn in progress or queued; interrupt it first")
	ErrThreadArchived     = errors.New("the conversation is archived; restore it before sending to it")
	ErrQueueFull          = fmt.Errorf("the conversation has %d messages waiting already; send this one once they start", maxQueued)
	ErrNoActiveTurn       = errors.New("the conversation has no turn in progress")
	ErrTurnMismatch       = errors.New("the turn in progress is not the one named")
	ErrNoSuchRequest      = errors.New("the conversation does not wait on that request")
	ErrInvalidMessage     = errors.New("invalid message")
	ErrInvalidAnswer      = errors.New("invalid answer")
	ErrInvalidDecision    = errors.New("invalid decision")
	ErrInvalidPath        = errors.New("invalid path")
	ErrEnvironmentInUse   = errors.New("a conversation or a workspace is in the environment")
	ErrFirstEnvironment   = errors.New("it is the first environment of the user, made with them, and stays")
	ErrInvalidEnvironment = errors.New("invalid environment")
	ErrInvalidQuery       = errors.New("invalid query")
	ErrInvalidTodo        = errors.New("invalid todo")
	ErrInvalidPresence    = errors.New("invalid presence")
	ErrSearchTimedOut     = errors.New("the search took too long; try longer or fewer words")
)

// Catalog is the deployment's models: the providers every user's turns run
// on. A user only chooses among them.
type Catalog interface {
	Providers() []account.ModelProvider
}

type Statuses func(user string) ([]account.MCPStatus, error)

type Attachments interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) error
}

type App struct {
	st          store.Store
	catalog     Catalog
	tools       tool.Tools
	statuses    Statuses
	envs        *environment.Boxes
	attachments Attachments
	cal         cron.Calendar
	deployment  policy.Settings
}

// New makes the app of a deployment whose conversations run in the
// environments of envs.
func New(st store.Store, catalog Catalog, tools tool.Tools, statuses Statuses, envs *environment.Boxes, attachments Attachments, cal cron.Calendar, deployment policy.Settings) *App {
	return &App{st: st, catalog: catalog, tools: tools, statuses: statuses, envs: envs, attachments: attachments, cal: cal, deployment: deployment}
}

// EnsureUser makes user, with their first environment, unless they are.
func (a *App) EnsureUser(ctx context.Context, user string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser(user, a.envs.First()) })
}

// Models are the deployment's models, the connection the user's
// conversations start with, and the commit they were read at.
func (a *App) Models(ctx context.Context, user string) (models []llm.Model, first string, seq int64, err error) {
	var s account.Settings
	var catalog []account.ModelProvider
	err = a.st.Read(ctx, func(r store.Reader) error {
		if s, err = r.Settings(user); err != nil {
			return err
		}
		if catalog, err = r.Catalog(); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	if err != nil {
		return nil, "", 0, err
	}
	models, first = account.Models(catalog, s)
	return models, first, seq, nil
}

func (a *App) Tools(ctx context.Context, user string) ([]tool.Spec, error) {
	set, err := a.tools(ctx, user)
	if err != nil {
		return nil, err
	}
	return set.Specs(), nil
}

func (a *App) pickModel(r store.Reader, user, connection string, last *llm.Model) (llm.Model, error) {
	s, err := r.Settings(user)
	if err != nil {
		return llm.Model{}, err
	}
	models, first := account.Models(a.catalog.Providers(), s)
	if len(models) == 0 {
		return llm.Model{}, fmt.Errorf("%w: there are no models to run; the deployment serves none", ErrInvalidMessage)
	}
	if connection == "" && last != nil {
		connection = last.Connection
	}
	if connection == "" {
		connection = first
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

func (a *App) toolbox(ctx context.Context, user string) (policy.Toolbox, error) {
	set, err := a.tools(ctx, user)
	if err != nil {
		return policy.Toolbox{}, fmt.Errorf("the tools of the user: %w", err)
	}
	return tool.Toolbox(set), nil
}

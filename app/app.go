// Package app is what a user can do: each command reads what it needs, checks it by the core's rules and writes it in
// one transaction; each query reads one snapshot. What a command changed reaches whoever watches by the store's feed,
// the scheduler among them, so a command tells no one.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
)

var (
	ErrWorkspaceBusy    = errors.New("a conversation in the workspace has a turn in progress or queued; interrupt it first")
	ErrThreadBusy       = errors.New("the conversation has a turn in progress or queued; interrupt it first")
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

// Models are the models a deployment offers, the first the default, and each one's limits.
type Models interface {
	Models() []llm.Model
	Limits(m llm.Model) (llm.Limits, error)
}

// Boxes gives a user's sandbox, where the paths they name are.
type Boxes func(user string) (sandbox.Box, error)

// Attachments keeps the files attached to messages, under their conversation's id.
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
	boxes       Boxes
	attachments Attachments
	cal         cron.Calendar
	deployment  policy.Settings

	// moving are the workspaces whose folder is moving, which take no new turn meanwhile.
	mu     sync.Mutex
	moving map[string]bool
}

// New takes the deployment's permission settings, beneath every user's.
func New(st store.Store, models Models, tools tool.Tools, boxes Boxes, attachments Attachments, cal cron.Calendar, deployment policy.Settings) *App {
	return &App{st: st, models: models, tools: tools, boxes: boxes, attachments: attachments, cal: cal, deployment: deployment, moving: map[string]bool{}}
}

func (a *App) EnsureUser(ctx context.Context, u string) error {
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser(u) })
}

// Models are the models a turn can run on, and the connection of the default one.
func (a *App) Models() ([]llm.Model, string) {
	models := a.models.Models()
	return models, models[0].Connection
}

// Tools are the tools a user's turns can be offered.
func (a *App) Tools(ctx context.Context, u string) ([]tool.Spec, error) {
	set, err := a.tools(ctx, u)
	if err != nil {
		return nil, err
	}
	return set.Specs(), nil
}

// model is the model a new turn runs on: the connection asked for, else the one the conversation ran on last, else the
// default.
func (a *App) model(connection string, last *llm.Model) (llm.Model, error) {
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

// thread is a user's conversation; another user's is refused as though it did not exist.
func thread(r store.Reader, u, id string) (store.Thread, error) {
	th, err := r.Thread(id)
	if err != nil {
		return store.Thread{}, err
	}
	if th.User != u {
		return store.Thread{}, fmt.Errorf("thread %s: %w", id, store.ErrNotFound)
	}
	return th, nil
}

// topThread is a user's own conversation, not a subagent's, which takes no input of theirs.
func topThread(r store.Reader, u, id string) (store.Thread, error) {
	th, err := thread(r, u, id)
	if err != nil {
		return store.Thread{}, err
	}
	if th.Parent != "" {
		return store.Thread{}, user.ErrSubagentThread
	}
	return th, nil
}

// toolbox is a user's tools as the permission rules see them.
func (a *App) toolbox(ctx context.Context, u string) (policy.Toolbox, error) {
	set, err := a.tools(ctx, u)
	if err != nil {
		return policy.Toolbox{}, fmt.Errorf("the tools of the user: %w", err)
	}
	box := policy.Toolbox{Unavailable: set.Unavailable()}
	for _, t := range set.Specs() {
		box.Tools = append(box.Tools, policy.Tool{Name: t.Name, Provider: t.Provider, Content: t.Content})
	}
	return box, nil
}

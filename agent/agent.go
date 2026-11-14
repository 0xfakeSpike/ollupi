package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/pkg/blobs"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

const (
	// maxAnswerRunes is the most an answer to one of the model's questions may say.
	maxAnswerRunes = 4000
	defaultSlots   = 32
	// maxQueuedTurns is how many turns one conversation may have waiting behind the one in progress.
	maxQueuedTurns  = 20
	defaultRunLimit = 4 * time.Hour
	// defaultBackgroundCalls and defaultBackgroundLimit bound the calls one user runs in the background.
	defaultBackgroundCalls = 4
	defaultBackgroundLimit = 4 * time.Hour
)

var (
	ErrNoActiveTurn      = errors.New("the thread has no turn in progress")
	ErrTurnMismatch      = errors.New("the expected turn is not the turn in progress")
	ErrNoSuchRequest     = errors.New("the thread does not wait on this request")
	ErrSubagentThread    = errors.New("a subagent thread takes no input; send it to its parent conversation")
	ErrWorkspaceArchived = errors.New("the workspace is archived; restore it before starting new work")
	ErrWorkspaceBusy     = errors.New("a conversation in the workspace has a turn in progress or queued; interrupt it first")
	ErrThreadArchived    = errors.New("the conversation is archived; restore it before starting new work")
	ErrThreadBusy        = errors.New("the conversation has a turn in progress or queued; interrupt it first")
	ErrQueueFull         = fmt.Errorf("the conversation has %d messages waiting already; send this one once they start", maxQueuedTurns)
	ErrInvalidMessage    = errors.New("invalid message")
	ErrInvalidAnswer     = errors.New("invalid answer")
	ErrInvalidDecision   = errors.New("invalid decision")
	ErrInvalidName       = errors.New("invalid name")
	ErrInvalidPath       = errors.New("invalid path")
)

type Config struct {
	Name     string
	Prompt   string
	Models   []llm.Model
	Tools    []string
	Settings permission.Settings
	Slots    int
	// UserSlots is the most slots one user may hold, so that one user's turns cannot fill a shard; by default, all.
	UserSlots int
	// RunLimit is how long a turn may run in its slot before it fails; by default, four hours.
	RunLimit time.Duration
	// BackgroundCalls is the most calls one user may run in the background at once; by default, four. A background
	// call holds no slot.
	BackgroundCalls int
	// BackgroundLimit is how long a background call may run before it fails; by default, four hours.
	BackgroundLimit time.Duration

	Connections map[string]llm.Client
	// ClientFor gives the client a user's turns call a connection through, so that they run on the user's own key
	// and quota; it should return the same client each time. Without it users share Connections.
	ClientFor func(user, connection string) (llm.Client, error)
	// Providers are the tools every user has.
	Providers []tools.Provider
	// ToolsFor adds the tools one user has, such as their MCP servers.
	ToolsFor  func(user string) (*tools.Set, error)
	SkillsDir string
	// Attachments keeps the images and files sent with messages; without it messages take none.
	Attachments blobs.Store
	// AttachmentsDir is a folder on this machine to keep them in, when Attachments is not set.
	AttachmentsDir string
	Memory         *memory.Manager
	DB             *repo.DB
	OnEvent        func(Event)
	// Lease makes this process claim the shard before it runs it, waiting for as long as another holds it, and keep
	// the claim alive; a process that dies leaves its claim for this long. Without it the process trusts it is alone.
	Lease time.Duration
	// BoxFor gives the sandbox a user's files live in on this machine, which the agent reaches workspaces and
	// attachments through. A conversation started in another environment reaches its own box: the workspace tools
	// take that from the call's tools.Caller. By default it is sandbox.Here.
	BoxFor func(user string) (sandbox.Box, error)
	// OnLeaseLost tells the host that another process took the shard: the agent has stopped, and all else on DB must.
	OnLeaseLost func()
}

type Agent struct {
	cfg      Config
	ctx      context.Context
	registry *tools.Registry
	skills   *skills.Catalog

	// providers are what every user's registry starts from.
	providers  []tools.Provider
	registries sync.Map

	// backgroundCancels stop the background calls running on this process, by id.
	backgroundMu      sync.Mutex
	backgroundCancels map[string]context.CancelFunc

	// moving holds the workspaces whose folder is being moved, which take no new turn meanwhile.
	moveMu sync.Mutex
	moving map[string]bool

	events       chan func()
	slotByThread map[string]*slot
	slotsOfUser  map[string]int
	owner        string
	done         sync.WaitGroup
}

func (a *Agent) ListTools(user string) ([]tools.ToolDescriptor, error) {
	registry, err := a.registryFor(user)
	if err != nil {
		return nil, err
	}
	return registry.Descriptors(), nil
}

// userRegistry is a user's registry and the Set it was built from.
type userRegistry struct {
	set      *tools.Set
	registry *tools.Registry
}

// registryFor gives the tools a user has. The first call may wait for their servers to connect.
func (a *Agent) registryFor(user string) (*tools.Registry, error) {
	if a.cfg.ToolsFor == nil {
		return a.registry, nil
	}
	set, err := a.cfg.ToolsFor(user)
	if err != nil {
		return nil, fmt.Errorf("agent: the tools of the user: %w", err)
	}
	if built, ok := a.registries.Load(user); ok && built.(userRegistry).set == set {
		return built.(userRegistry).registry, nil
	}
	registry, err := tools.New(a.ctx, set.Unavailable, slices.Concat(a.providers, set.Providers)...)
	if err != nil {
		return nil, fmt.Errorf("agent: the tools of the user: %w", err)
	}
	a.registries.Store(user, userRegistry{set: set, registry: registry})
	return registry, nil
}

func New(cfg Config) *Agent {
	if cfg.BoxFor == nil {
		cfg.BoxFor = func(string) (sandbox.Box, error) { return sandbox.Here(), nil }
	}
	return &Agent{cfg: cfg, moving: map[string]bool{}, backgroundCancels: map[string]context.CancelFunc{}}
}

func (a *Agent) Start(ctx context.Context) error {
	if strings.TrimSpace(a.cfg.Name) == "" {
		return fmt.Errorf("agent: name is required")
	}
	if a.cfg.DB == nil {
		return fmt.Errorf("agent: a database is required")
	}
	if err := a.buildTools(ctx); err != nil {
		return err
	}
	if err := a.checkModels(); err != nil {
		return err
	}
	if err := a.checkPromptFits(); err != nil {
		return fmt.Errorf("agent %s: %w", a.cfg.Name, err)
	}
	if a.cfg.Slots <= 0 {
		a.cfg.Slots = defaultSlots
	}
	if a.cfg.UserSlots <= 0 {
		a.cfg.UserSlots = a.cfg.Slots
	}
	if a.cfg.RunLimit <= 0 {
		a.cfg.RunLimit = defaultRunLimit
	}
	if a.cfg.BackgroundCalls <= 0 {
		a.cfg.BackgroundCalls = defaultBackgroundCalls
	}
	if a.cfg.BackgroundLimit <= 0 {
		a.cfg.BackgroundLimit = defaultBackgroundLimit
	}
	if a.cfg.Attachments == nil && a.cfg.AttachmentsDir != "" {
		a.cfg.Attachments = blobs.Dir(a.cfg.AttachmentsDir)
	}
	a.events, a.slotByThread, a.slotsOfUser = make(chan func()), map[string]*slot{}, map[string]int{}
	a.ctx = ctx
	if a.cfg.Lease > 0 {
		var err error
		if a.ctx, err = a.claimShard(ctx); err != nil {
			return err
		}
	}
	if err := a.recover(); err != nil {
		return err
	}
	if _, err := a.cfg.DB.InterruptBackgroundCalls(time.Now()); err != nil {
		return fmt.Errorf("agent: end the background calls of a process that stopped: %w", err)
	}
	a.done.Add(1)
	go a.loop()
	slog.Info("agent started", "name", a.cfg.Name, "slots", a.cfg.Slots, "models", len(a.cfg.Models), "tools", len(a.registry.Descriptors()))
	return nil
}

func (a *Agent) buildTools(ctx context.Context) error {
	providers := append(slices.Clone(a.cfg.Providers), plan.New(), role.Launchers{}, userinput.New(), contextmanager.NewOutputs(a.cfg.DB), runtime.Loader{}, backgroundTools{a})
	if a.cfg.Memory != nil {
		providers = append(providers, a.cfg.Memory)
	}
	if a.cfg.SkillsDir != "" {
		var err error
		if a.skills, err = skills.New(a.cfg.SkillsDir); err != nil {
			return fmt.Errorf("agent: skills: %w", err)
		}
		providers = append(providers, a.skills)
	}
	a.providers = providers
	var err error
	if a.registry, err = tools.New(ctx, nil, providers...); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

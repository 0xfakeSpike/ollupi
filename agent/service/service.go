// Package service is what clients ask of a shard: its conversations, workspaces and environments, the turns sent to
// them and the answers to what they ask, the models and tools there are. It reads and writes them in the data layer
// itself, and tells the orchestrator, which schedules the turns into slots and runs them, of what it has to look at.
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/repo/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/background"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	skilltools "github.com/0xfakeSpike/ollupi/tools/skills"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

const (
	// maxAnswerRunes is the most an answer to one of the model's questions may say.
	maxAnswerRunes = 4000
	// maxQueuedTurns is how many turns one conversation may have waiting behind the one in progress.
	maxQueuedTurns = 20
)

var (
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

// Config is what a shard of ollupi runs with.
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
	ToolsFor func(user string) (*tools.Set, error)
	// SkillsDir holds the skills of each role, in the directory of the role's name: SkillsDir/<role>/<skill>/SKILL.md.
	SkillsDir string
	// Attachments keeps the images and files sent with messages; without it messages take none.
	Attachments blobs.Store
	// AttachmentsDir is a folder on this machine to keep them in, when Attachments is not set.
	AttachmentsDir string
	Memory         *memory.Manager
	DB             *db.DB
	OnEvent        func(event.Event)
	// Lease makes this process claim the shard before it runs it, waiting for as long as another holds it, and keep
	// the claim alive; a process that dies leaves its claim for this long. Without it the process trusts it is alone.
	Lease time.Duration
	// BoxFor gives the sandbox a user's files live in on this machine, which the service reaches workspaces and
	// attachments through. A conversation started in another environment reaches its own box: the workspace tools
	// take that from the call's tools.Caller. By default it is sandbox.Here.
	BoxFor func(user string) (sandbox.Box, error)
	// OnLeaseLost tells the host that another process took the shard: the shard has stopped, and all else on DB must.
	OnLeaseLost func()
}

// A Service is a shard of ollupi as its clients see it.
type Service struct {
	cfg Config
	ctx context.Context
	o   *orchestrator.Orchestrator
	res *resources.Resources
	ev  event.Emitter

	// moving holds the workspaces whose folder is being moved, which take no new turn meanwhile.
	moveMu sync.Mutex
	moving map[string]bool
}

func New(cfg Config) *Service {
	if cfg.BoxFor == nil {
		cfg.BoxFor = func(string) (sandbox.Box, error) { return sandbox.Here(), nil }
	}
	if cfg.Attachments == nil && cfg.AttachmentsDir != "" {
		cfg.Attachments = blobs.Dir(cfg.AttachmentsDir)
	}
	o := orchestrator.New(orchestrator.Config{
		DB: cfg.DB, Attachments: cfg.Attachments, Memory: cfg.Memory, OnEvent: cfg.OnEvent,
		Runtime: runtime.Config{
			Name: cfg.Name, Prompt: cfg.Prompt, SkillsDir: cfg.SkillsDir, Settings: cfg.Settings, Shared: cfg.ToolsFor != nil, Tools: cfg.Tools,
		},
		Slots: cfg.Slots, UserSlots: cfg.UserSlots, RunLimit: cfg.RunLimit,
		BackgroundCalls: cfg.BackgroundCalls, BackgroundLimit: cfg.BackgroundLimit,
		Lease: cfg.Lease, OnLeaseLost: cfg.OnLeaseLost,
	})
	return &Service{cfg: cfg, o: o, ev: event.Emitter{DB: cfg.DB, OnEvent: cfg.OnEvent}, moving: map[string]bool{}}
}

// Start makes what turns run on, checks that each model takes the main agent's prompt, and starts the orchestrator.
func (sv *Service) Start(ctx context.Context) error {
	if strings.TrimSpace(sv.cfg.Name) == "" {
		return fmt.Errorf("service: name is required")
	}
	if sv.cfg.DB == nil {
		return fmt.Errorf("service: a database is required")
	}
	providers := append(slices.Clone(sv.cfg.Providers), plan.New(), subagent.New(sv.cfg.DB), userinput.New(),
		contextmanager.NewOutputs(sv.cfg.DB), runtime.Loader{}, background.New(sv.cfg.DB, sv.o.CancelBackground))
	if sv.cfg.Memory != nil {
		providers = append(providers, sv.cfg.Memory)
	}
	if sv.cfg.SkillsDir != "" {
		var roles []string
		for _, name := range role.NameValues {
			roles = append(roles, string(name))
		}
		if err := skills.Check(sv.cfg.SkillsDir, roles); err != nil {
			return fmt.Errorf("service: %w", err)
		}
		providers = append(providers, skilltools.New(sv.cfg.SkillsDir))
	}
	res, err := resources.New(ctx, resources.Config{
		Models: sv.cfg.Models, Connections: sv.cfg.Connections, ClientFor: sv.cfg.ClientFor, ToolsFor: sv.cfg.ToolsFor,
	}, providers)
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	sv.ctx, sv.res = ctx, res
	if err := sv.checkPromptFits(); err != nil {
		return fmt.Errorf("agent %s: %w", sv.cfg.Name, err)
	}
	return sv.o.Start(ctx, res)
}

// Wait waits for the shard to stop, once the context Start was given ends.
func (sv *Service) Wait() { sv.o.Wait() }

// checkPromptFits checks that each model's input takes its main agent's system prompt and tools.
func (sv *Service) checkPromptFits() error {
	checker, err := permission.New(permission.Base(sv.cfg.Settings, sv.res.Shared(), db.WorkspaceRow{}), sv.cfg.Tools, sv.res.Tools())
	if err != nil {
		return err
	}
	descriptors := checker.Tools()
	models, _ := sv.res.Models()
	for _, m := range models {
		window, err := sv.res.Window(m)
		if err != nil {
			return err
		}
		main := role.ForMain(window.InputTokens())
		offered, deferred := main.Offer(descriptors, window, nil)
		system, err := main.System(role.Input{
			Name: sv.cfg.Name, Prompt: sv.cfg.Prompt, SkillsDir: sv.cfg.SkillsDir, SkillTokens: window.SkillBudget(), Deferred: deferred,
		})
		if err != nil {
			return err
		}
		if err := window.Fits(system, role.Specs(offered)); err != nil {
			return err
		}
	}
	return nil
}

func (sv *Service) ListModels() ([]llm.Model, string) { return sv.res.Models() }

func (sv *Service) ListTools(user string) ([]tools.ToolDescriptor, error) {
	registry, err := sv.res.Registry(user)
	if err != nil {
		return nil, err
	}
	return registry.Descriptors(), nil
}

// conversationModel is the model a turn of the conversation runs on: the one asked for, or else its last turn's, or
// else the default.
func (sv *Service) conversationModel(last *db.TurnRow, requested string) (llm.Model, error) {
	connection := strings.TrimSpace(requested)
	if connection == "" && last != nil {
		connection = last.Model.Connection
	}
	if connection == "" {
		_, connection = sv.res.Models()
	}
	model, err := sv.res.Model(connection)
	if err != nil {
		return llm.Model{}, fmt.Errorf("service: %w: %v", ErrInvalidMessage, err)
	}
	return model, nil
}

// mainRole is the main role of a turn on the model, by how much input it takes.
func (sv *Service) mainRole(model llm.Model) (role.Role, error) {
	window, err := sv.res.Window(model)
	if err != nil {
		return role.Role{}, err
	}
	return role.ForMain(window.InputTokens()), nil
}

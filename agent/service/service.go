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

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/repo/skills"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
	"github.com/0xfakeSpike/ollupi/tools/outputs"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	skilltools "github.com/0xfakeSpike/ollupi/tools/skills"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

const (
	maxAnswerRunes    = 4000
	maxQueuedMessages = 20
)

var (
	ErrSubagentThread    = errors.New("a subagent thread takes no input; send it to its parent conversation")
	ErrWorkspaceArchived = errors.New("the workspace is archived; restore it before starting new work")
	ErrWorkspaceBusy     = errors.New("a conversation in the workspace has a turn in progress or queued; interrupt it first")
	ErrQueueFull         = fmt.Errorf("the conversation has %d messages waiting already; send this one once they start", maxQueuedMessages)
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
	// Caps one user so their turns cannot fill a shard; by default, all.
	UserSlots int
	// By default, four hours.
	RunLimit time.Duration

	Connections map[string]llm.Client
	// Runs a user's turns on their own key and quota; it should return the same client each time.
	ClientFor func(user, connection string) (llm.Client, error)
	Providers []tools.Provider
	// Without it scheduled tasks are unavailable.
	Jobs *cron.Jobs
	// Layout: SkillsDir/<role>/<skill>/SKILL.md.
	SkillsDir   string
	Attachments blobs.Store
	Memory      *memory.Manager
	DB          *db.DB
	Events      *event.Bus
	// A process that dies leaves its claim for this long. Without it the process trusts it is alone.
	Lease time.Duration
	// Required: a client names the paths it reads, so a one-user host passes sandbox.Here, and no host gives
	// every user this machine by leaving it out.
	BoxFor func(user string) (sandbox.Box, error)
	// The shard has stopped by then, and all else on DB must.
	OnLeaseLost func()
}

type Service struct {
	cfg         Config
	permissions *permission.Manager
	o           *orchestrator.Orchestrator
	res         *resources.Resources
	mcp         *mcp.Users
	ev          *event.Bus

	moveMu sync.Mutex
	moving map[string]bool
}

func New(cfg Config) *Service {
	return &Service{cfg: cfg, ev: cfg.Events, moving: map[string]bool{}}
}

func (sv *Service) Start(ctx context.Context) error {
	if strings.TrimSpace(sv.cfg.Name) == "" {
		return fmt.Errorf("service: name is required")
	}
	if sv.cfg.DB == nil {
		return fmt.Errorf("service: a database is required")
	}
	if sv.cfg.BoxFor == nil {
		return fmt.Errorf("service: BoxFor is required")
	}
	if sv.cfg.Events == nil {
		return fmt.Errorf("service: Events is required")
	}
	deployment := permission.Deployment{Settings: sv.cfg.Settings, Tools: sv.cfg.Tools}
	sv.o = orchestrator.New(sv.cfg.DB, sv.cfg.Attachments, sv.cfg.Memory, sv.cfg.Events, sv.cfg.Name, sv.cfg.Prompt, sv.cfg.SkillsDir,
		deployment, sv.cfg.Slots, sv.cfg.UserSlots, sv.cfg.RunLimit)
	providers := append(slices.Clone(sv.cfg.Providers), plan.New(), subagent.New(sv.cfg.DB, sv.o.Started), userinput.New(),
		outputs.New(sv.cfg.DB), loader.New())
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
	sv.mcp = mcp.NewUsers(ctx, mcp.Client{Name: sv.cfg.Name}, sv.mcpServers, sv.cfg.BoxFor)
	res, err := resources.New(ctx, resources.Config{
		Models: sv.cfg.Models, Connections: sv.cfg.Connections, ClientFor: sv.cfg.ClientFor, ToolsFor: sv.mcp.Tools,
	}, providers)
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	sv.res = res
	sv.permissions = permission.NewManager(deployment, sv.cfg.DB, res.Registry, sv.cfg.Events)
	if sv.cfg.Lease > 0 {
		if ctx, err = sv.cfg.DB.Claim(ctx, sv.cfg.Lease, sv.cfg.OnLeaseLost); err != nil {
			return fmt.Errorf("service: %w", err)
		}
	}
	if err := sv.o.Start(ctx, res); err != nil {
		sv.cfg.DB.Release()
		return err
	}
	return nil
}

func (sv *Service) Wait() {
	sv.o.Wait()
	sv.mcp.Close()
	sv.cfg.DB.Release()
}

func (sv *Service) ListModels() ([]llm.Model, string) { return sv.res.Models() }

func (sv *Service) ListTools(user string) ([]tools.ToolDescriptor, error) {
	registry, err := sv.res.Registry(user)
	if err != nil {
		return nil, err
	}
	return registry.Descriptors(), nil
}

func (sv *Service) status(threadID string) {
	if t, status, err := sv.cfg.DB.TopThreadStatus(threadID); err == nil {
		sv.ev.Status(t, status)
	}
}

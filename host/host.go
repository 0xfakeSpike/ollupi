// Package host puts ollupi together into one process: the store, the models, every user's tools, the flows that run
// turns, learn memory and send scheduled tasks, the app, and the gateway clients reach it by. A host serves alone, or
// as one shard of a deployment, which shard.Member decides; the parts know nothing of which.
package host

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/blob"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/extract"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/prompt"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/schedule"
	"github.com/0xfakeSpike/ollupi/skill"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool/builtin/ask"
	"github.com/0xfakeSpike/ollupi/tool/builtin/loader"
	memorytool "github.com/0xfakeSpike/ollupi/tool/builtin/memory"
	"github.com/0xfakeSpike/ollupi/tool/builtin/outputs"
	"github.com/0xfakeSpike/ollupi/tool/builtin/plan"
	"github.com/0xfakeSpike/ollupi/tool/builtin/skills"
	"github.com/0xfakeSpike/ollupi/tool/builtin/subagent"
	"github.com/0xfakeSpike/ollupi/tool/builtin/tasks"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/0xfakeSpike/ollupi/tool/mcp"
	"golang.org/x/sync/errgroup"
)

const (
	defaultSlots    = 32
	defaultRunLimit = 4 * time.Hour
)

type Config struct {
	// Home holds the store and the attachments.
	Home string
	// Name is what the agent calls itself, and Prompt the main agents' prompt, "" for the default one.
	Name   string
	Prompt string
	// Models are those turns run on, the first the default; each names its connection, one model to a connection.
	Models      []llm.Model
	Connections map[string]llm.Client
	// ClientFor, when set, gives the client a user's turns call a connection through, so that they run on the user's
	// own key; it should give the same client each time.
	ClientFor func(user, connection string) (llm.Client, error)
	// Memory is the model extraction asks, on one of Connections; nil learns no memory.
	Memory *llm.Model
	// Tools are the deployment's own beside the built-in ones, such as the workspace's and the web's.
	Tools []kit.Provider
	// Allowlist, unless nil, is every tool the deployment lets be offered.
	Allowlist []string
	// Permissions are the deployment's, beneath every user's.
	Permissions policy.Settings
	// Boxes gives a user's sandbox, where their paths are and their MCP servers run. Required: a host with one user
	// passes sandbox.Here, and none gives every user this machine by leaving it out.
	Boxes func(user string) (sandbox.Box, error)
	// SkillsDir holds the skills, as SkillsDir/<role>/<skill>/SKILL.md; "" has none.
	SkillsDir string
	// Slots is how many turns run at once, 32 by default; UserSlots how many of one user's, all by default.
	Slots     int
	UserSlots int
	// RunLimit is how long a turn runs at most, four hours by default.
	RunLimit time.Duration
	// Location is the zone scheduled tasks read their specs in, the local one by default.
	Location *time.Location
	// Gateway, when set, serves clients; a host without one is driven by its App, as one that runs a benchmark is.
	Gateway *gateway.Options
}

type Host struct {
	Store store.Store
	App   *app.App
	Users *mcp.Users

	runner    *run.Runner
	scheduler *schedule.Scheduler
	extractor *extract.Extractor
	cal       cron.Calendar
	// Gateway is the server clients reach the host by, nil without Config.Gateway.
	Gateway *gateway.Server
}

// New puts a host together within ctx, which its users' MCP servers run in.
func New(ctx context.Context, cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("host: a name is required")
	}
	if cfg.Boxes == nil {
		return nil, errors.New("host: Boxes is required")
	}
	ms, err := newModels(cfg.Models, cfg.Connections, cfg.ClientFor)
	if err != nil {
		return nil, err
	}
	if cfg.SkillsDir != "" {
		roles := make([]string, len(agent.Names))
		for i, n := range agent.Names {
			roles[i] = string(n)
		}
		if err := skill.Check(cfg.SkillsDir, roles); err != nil {
			return nil, fmt.Errorf("host: %w", err)
		}
	}
	st, err := sqlite.Open(cfg.Home)
	if err != nil {
		return nil, err
	}
	files := blob.Dir(filepath.Join(cfg.Home, "attachments"))
	location := cmp.Or(cfg.Location, time.Local)
	h := &Host{Store: st, cal: cron.NewCalendar(location)}
	builtins := append(slices.Clone(cfg.Tools), plan.Provider{}, ask.Provider{}, loader.Provider{}, subagent.Provider{},
		outputs.New(st), memorytool.New(st))
	if cfg.SkillsDir != "" {
		builtins = append(builtins, skills.New(cfg.SkillsDir))
	}
	// The scheduled-task tools are the app's commands, and the app needs the users' tools: the tools come first, and
	// reach the app through jobs once it is made.
	jobs := &jobs{}
	builtins = append(builtins, tasks.New(jobs, location))
	h.Users = mcp.NewUsers(ctx, mcp.Client{Name: cfg.Name, Version: version()}, st, cfg.Boxes, builtins...)
	h.App = app.New(st, ms, h.Users.Tools, cfg.Boxes, files, h.cal, cfg.Permissions)
	jobs.App = h.App
	h.runner = run.New(st, ms, h.Users.Tools, files, skillsOf(cfg.SkillsDir), cfg.Name, cfg.Prompt, cfg.Permissions, cfg.Allowlist)
	slots := cmp.Or(cfg.Slots, defaultSlots)
	h.scheduler = schedule.New(st, h.runner, slots, cmp.Or(cfg.UserSlots, slots), cmp.Or(cfg.RunLimit, defaultRunLimit))
	if cfg.Memory != nil {
		limits, err := ms.Limits(*cfg.Memory)
		if err != nil {
			h.Close()
			return nil, fmt.Errorf("host: the memory model: %w", err)
		}
		model := *cfg.Memory
		client := func(u string) (llm.Client, error) { return ms.Client(u, model) }
		if h.extractor, err = extract.New(st, model, limits, client); err != nil {
			h.Close()
			return nil, err
		}
	}
	if cfg.Gateway != nil {
		if h.Gateway, err = gateway.NewServer(h.App, st.Feed(), h.Users, *cfg.Gateway); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

// jobs are the app's task commands, for the tools that schedule tasks.
type jobs struct{ *app.App }

func skillsOf(dir string) run.Skills {
	return func(role agent.Name) ([]prompt.Skill, error) {
		if dir == "" {
			return nil, nil
		}
		found, err := skill.Load(dir, string(role))
		out := make([]prompt.Skill, len(found))
		for i, s := range found {
			out[i] = prompt.Skill{Name: s.Name, Description: s.Description}
		}
		return out, err
	}
}

// Run runs the flows until ctx ends or one fails: the scheduler, memory extraction, and scheduled tasks.
func (h *Host) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return h.scheduler.Run(ctx) })
	g.Go(func() error { return cron.Run(ctx, h.Store, h.cal, h.App.Scheduled) })
	if h.extractor != nil {
		g.Go(func() error { return h.extractor.Run(ctx) })
	}
	err := g.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (h *Host) Close() error {
	return errors.Join(h.Users.Close(), h.Store.Close())
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

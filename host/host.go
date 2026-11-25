package host

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/blob"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/environment"
	"github.com/0xfakeSpike/ollupi/extract"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/schedule"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool/builtin/ask"
	"github.com/0xfakeSpike/ollupi/tool/builtin/cronjobs"
	"github.com/0xfakeSpike/ollupi/tool/builtin/hub"
	"github.com/0xfakeSpike/ollupi/tool/builtin/loader"
	memorytool "github.com/0xfakeSpike/ollupi/tool/builtin/memory"
	"github.com/0xfakeSpike/ollupi/tool/builtin/outputs"
	"github.com/0xfakeSpike/ollupi/tool/builtin/plan"
	"github.com/0xfakeSpike/ollupi/tool/builtin/skills"
	"github.com/0xfakeSpike/ollupi/tool/builtin/subagent"
	"github.com/0xfakeSpike/ollupi/tool/builtin/web"
	"github.com/0xfakeSpike/ollupi/tool/builtin/workspace"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/0xfakeSpike/ollupi/tool/mcp"
	"github.com/0xfakeSpike/ollupi/usage"
	"golang.org/x/sync/errgroup"
)

const (
	defaultSlots         = 32
	defaultRunLimit      = 4 * time.Hour
	defaultTaskSlots     = 32
	defaultUserTaskSlots = 4
	defaultTaskLimit     = time.Hour
)

type Config struct {
	Home   string
	Name   string
	Prompt string
	// Dial makes the client of each model; nil calls the provider's API.
	Dial Dial
	// ClientFor, when set, picks the client a user's call goes through
	// instead of the catalog's, as a deployment that bills each user's own
	// account would.
	ClientFor   func(user, connection string) (llm.Client, error)
	Tools       []kit.Provider
	Hub         bool
	OptIn       []string
	Allowlist   []string
	Permissions policy.Settings
	// Sandboxes are the providers a user's environments are sandboxes of,
	// by id: this machine, a cloud's. Sandbox names the one every user's
	// first environment is a sandbox of.
	Sandboxes map[string]sandbox.Provider
	Sandbox   string
	// Commands is how commands run in every environment: timeouts and the
	// commands never run.
	Commands      workspace.Config
	Slots         int
	UserSlots     int
	RunLimit      time.Duration
	TaskSlots     int
	UserTaskSlots int
	TaskLimit     time.Duration
	Location      *time.Location
	Gateway       *gateway.Options
}

type Host struct {
	Store store.Store
	App   *app.App
	Users *mcp.Users

	models    *models
	envs      *environment.Boxes
	scheduler *schedule.Scheduler
	tasks     *schedule.Scheduler
	extractor *extract.Extractor
	usage     *usage.Recorder
	cal       cron.Calendar
	Gateway   *gateway.Server
}

func New(ctx context.Context, cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("host: a name is required")
	}
	cfg.Permissions.ApprovalPolicy = cmp.Or(cfg.Permissions.ApprovalPolicy, string(policy.OnRequest))
	if err := cfg.Permissions.Check(); err != nil {
		return nil, fmt.Errorf("host: the permissions: %w", err)
	}
	st, err := sqlite.Open(cfg.Home)
	if err != nil {
		return nil, err
	}
	modelSet, err := newModels(ctx, st, cfg.Dial, cfg.ClientFor)
	if err != nil {
		st.Close()
		return nil, err
	}
	envs, err := environment.New(st, cfg.Sandboxes, cfg.Sandbox)
	if err != nil {
		modelSet.close()
		st.Close()
		return nil, fmt.Errorf("host: %w", err)
	}
	commands, err := workspace.New(workspace.Options{Config: cfg.Commands, Boxes: envs.In})
	if err != nil {
		envs.Close()
		modelSet.close()
		st.Close()
		return nil, fmt.Errorf("host: %w", err)
	}
	files := blob.Dir(filepath.Join(cfg.Home, "attachments"))
	h := &Host{Store: st, models: modelSet, envs: envs, cal: cron.NewCalendar(cmp.Or(cfg.Location, time.Local))}
	hubTools := &hub.Provider{St: st}
	builtins := append(slices.Clone(cfg.Tools), commands, plan.Provider{}, ask.Provider{}, loader.Provider{}, subagent.Provider{},
		outputs.New(st), memorytool.New(st), web.New(envs.In), skills.New(st, envs.In))
	if cfg.Hub {
		builtins = append(builtins, hubTools)
	}
	builtins = append(builtins, cronjobs.New(st, h.cal))
	h.Users = mcp.NewUsers(ctx, mcp.Client{Name: cfg.Name, Version: version()}, st, envs.In, builtins...)
	h.App = app.New(st, modelSet, h.Users.Tools, h.Users.Statuses, envs, files, h.cal, cfg.Permissions)
	if err := h.App.ClearUploads(ctx); err != nil {
		slog.Warn("host: the uploads of an earlier run are left in the store", "error", err)
	}
	h.usage = usage.New(st)
	hubTools.App = h.App
	runner := run.New(st, modelSet, h.Users.Tools, files,
		run.Deployment{Name: cfg.Name, Prompt: cfg.Prompt, Permissions: cfg.Permissions, Allowlist: cfg.Allowlist, OptIn: cfg.OptIn,
			Environment: h.describe})
	slots := cmp.Or(cfg.Slots, defaultSlots)
	h.scheduler = schedule.New(st, schedule.Threads(runner.Run), slots, cmp.Or(cfg.UserSlots, slots), cmp.Or(cfg.RunLimit, defaultRunLimit))
	h.tasks = schedule.New(st, schedule.Tasks(runner.Task), cmp.Or(cfg.TaskSlots, defaultTaskSlots),
		cmp.Or(cfg.UserTaskSlots, defaultUserTaskSlots), cmp.Or(cfg.TaskLimit, defaultTaskLimit))
	h.extractor = extract.New(st, modelSet.memory,
		func(user string, m llm.Model) (llm.Client, error) { return modelSet.Client(user, m) })
	if cfg.Gateway != nil {
		if h.Gateway, err = gateway.NewServer(h.App, st.Feed(), *cfg.Gateway); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

func (h *Host) Run(ctx context.Context) error {
	if err := h.App.SettleMoves(ctx); err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return h.scheduler.Run(ctx) })
	g.Go(func() error { return h.tasks.Run(ctx) })
	g.Go(func() error { return cron.Run(ctx, h.Store, h.cal, h.App.Scheduled) })
	g.Go(func() error { return h.usage.Run(ctx) })
	g.Go(func() error { return h.extractor.Run(ctx) })
	err := g.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (h *Host) Close() error {
	h.models.close()
	return errors.Join(h.Users.Close(), h.envs.Close(), h.Store.Close())
}

// describe says where a conversation's commands and files are, for its
// system prompt.
func (h *Host) describe(ctx context.Context, user, id string) (string, error) {
	var env account.Environment
	err := h.Store.Read(ctx, func(r store.Reader) (err error) { env, err = r.Environment(user, id); return err })
	if err != nil {
		return "", err
	}
	box, err := h.envs.In(ctx, user, env.ID)
	if err != nil {
		return "", err
	}
	return workspace.Describe(env.Name, box), nil
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

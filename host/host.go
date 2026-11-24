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

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/blob"
	"github.com/0xfakeSpike/ollupi/cron"
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
	// Models are the deployment's catalog: the providers, with their keys,
	// and the models every user's turns run on. A user only chooses among
	// them; the host is never told about users. SetModels replaces it.
	Models []account.ModelProvider
	// Dial makes the client of each model; nil calls the provider's API.
	Dial Dial
	// ClientFor, when set, picks the client a user's call goes through
	// instead of the catalog's, as a deployment that bills each user's own
	// account would.
	ClientFor     func(user, connection string) (llm.Client, error)
	Tools         []kit.Provider
	Hub           bool
	OptIn         []string
	Allowlist     []string
	Permissions   policy.Settings
	Boxes         func(user string) (sandbox.Box, error)
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
	if cfg.Boxes == nil {
		return nil, errors.New("host: Boxes is required")
	}
	cfg.Permissions.ApprovalPolicy = cmp.Or(cfg.Permissions.ApprovalPolicy, string(policy.OnRequest))
	if err := cfg.Permissions.Check(); err != nil {
		return nil, fmt.Errorf("host: the permissions: %w", err)
	}
	st, err := sqlite.Open(cfg.Home)
	if err != nil {
		return nil, err
	}
	modelSet, err := newModels(ctx, st, cfg.Models, cfg.Dial, cfg.ClientFor)
	if err != nil {
		st.Close()
		return nil, err
	}
	files := blob.Dir(filepath.Join(cfg.Home, "attachments"))
	h := &Host{Store: st, models: modelSet, cal: cron.NewCalendar(cmp.Or(cfg.Location, time.Local))}
	hubTools := &hub.Provider{St: st}
	builtins := append(slices.Clone(cfg.Tools), plan.Provider{}, ask.Provider{}, loader.Provider{}, subagent.Provider{},
		outputs.New(st), memorytool.New(st), web.New(cfg.Boxes), skills.New(st, cfg.Boxes))
	if cfg.Hub {
		builtins = append(builtins, hubTools)
	}
	builtins = append(builtins, cronjobs.New(st, h.cal))
	h.Users = mcp.NewUsers(ctx, mcp.Client{Name: cfg.Name, Version: version()}, st, cfg.Boxes, builtins...)
	h.App = app.New(st, modelSet, h.Users.Tools, h.Users.Statuses, cfg.Boxes, files, h.cal, cfg.Permissions)
	h.usage = usage.New(st)
	hubTools.App = h.App
	runner := run.New(st, modelSet, h.Users.Tools, files,
		run.Deployment{Name: cfg.Name, Prompt: cfg.Prompt, Permissions: cfg.Permissions, Allowlist: cfg.Allowlist, OptIn: cfg.OptIn})
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

// SetModels replaces the deployment's catalog while the host runs: new
// turns run on it at once, and a turn already calling a model finishes on
// its client. A catalog that does not check leaves the one in use. A user's
// choice of a model no longer served falls back to the first.
func (h *Host) SetModels(catalog []account.ModelProvider) error {
	return h.models.set(catalog)
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

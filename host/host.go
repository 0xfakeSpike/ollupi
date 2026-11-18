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
	Home        string
	Name        string
	Prompt      string
	Models      []llm.Model
	Connections map[string]llm.Client
	ClientFor   func(user, connection string) (llm.Client, error)
	Memory      *llm.Model
	Tools       []kit.Provider
	Allowlist   []string
	Permissions policy.Settings
	Boxes       func(user string) (sandbox.Box, error)
	SkillsDir   string
	Slots       int
	UserSlots   int
	RunLimit    time.Duration
	Location    *time.Location
	Gateway     *gateway.Options
}

type Host struct {
	Store store.Store
	App   *app.App
	Users *mcp.Users

	scheduler *schedule.Scheduler
	extractor *extract.Extractor
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
	h := &Host{Store: st, cal: cron.NewCalendar(cmp.Or(cfg.Location, time.Local))}
	builtins := append(slices.Clone(cfg.Tools), plan.Provider{}, ask.Provider{}, loader.Provider{}, subagent.Provider{},
		outputs.New(st), memorytool.New(st))
	if cfg.SkillsDir != "" {
		builtins = append(builtins, skills.New(cfg.SkillsDir))
	}
	builtins = append(builtins, tasks.New(st, h.cal))
	h.Users = mcp.NewUsers(ctx, mcp.Client{Name: cfg.Name, Version: version()}, st, cfg.Boxes, builtins...)
	h.App = app.New(st, ms, h.Users.Tools, h.Users.Statuses, cfg.Boxes, files, h.cal, cfg.Permissions)
	runner := run.New(st, ms, h.Users.Tools, files, skillsOf(cfg.SkillsDir),
		run.Deployment{Name: cfg.Name, Prompt: cfg.Prompt, Permissions: cfg.Permissions, Allowlist: cfg.Allowlist})
	slots := cmp.Or(cfg.Slots, defaultSlots)
	h.scheduler = schedule.New(st, runner, slots, cmp.Or(cfg.UserSlots, slots), cmp.Or(cfg.RunLimit, defaultRunLimit))
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
		if h.Gateway, err = gateway.NewServer(h.App, st.Feed(), *cfg.Gateway); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

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

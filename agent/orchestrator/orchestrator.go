package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

const (
	defaultSlots    = 32
	defaultRunLimit = 4 * time.Hour
)

// Config is what the orchestrator schedules and runs turns with.
type Config struct {
	DB *db.DB
	// Attachments keeps the files sent with messages, and Memory the users' memory; runs read them.
	Attachments blobs.Store
	Memory      *memory.Manager
	OnEvent     func(event.Event)
	// Name and Prompt make the agent's own prompt, and SkillsDir is where each role's skills are installed: every run
	// goes by them.
	Name, Prompt, SkillsDir string
	// Settings are the default permission settings of every run, which serve users with different tools when Shared.
	// Tools, when set, are the only tools a turn may use.
	Settings permission.Settings
	Shared   bool
	Tools    []string
	Slots    int
	// UserSlots is the most slots one user may hold, so that one user's turns cannot fill a shard; by default, all.
	UserSlots int
	// RunLimit is how long a turn may run in its slot before it fails; by default, four hours.
	RunLimit time.Duration
	// Lease makes this process claim the shard before it runs it, waiting for as long as another holds it, and keep
	// the claim alive; a process that dies leaves its claim for this long. Without it the process trusts it is alone.
	Lease time.Duration
	// OnLeaseLost tells the host that another process took the shard: the orchestrator has stopped, and all else on DB must.
	OnLeaseLost func()
}

// An Orchestrator schedules the turns of a shard into slots and runs them, and launches subagents. // calls. What clients ask of the shard is the service's; it tells the orchestrator what to look at.
type Orchestrator struct {
	cfg Config
	ctx context.Context
	res *resources.Resources
	ev  event.Emitter

	events       chan func()
	slotByThread map[string]*slot
	slotsOfUser  map[string]int
	owner        string
	done         sync.WaitGroup
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{cfg: cfg, ev: event.Emitter{DB: cfg.DB, OnEvent: cfg.OnEvent}}
}

// Start runs the turns of the shard on res until ctx ends: it ends what a process that stopped left running, and
// takes the turns that are ready.
func (o *Orchestrator) Start(ctx context.Context, res *resources.Resources) error {
	o.res = res
	if o.cfg.Slots <= 0 {
		o.cfg.Slots = defaultSlots
	}
	if o.cfg.UserSlots <= 0 {
		o.cfg.UserSlots = o.cfg.Slots
	}
	if o.cfg.RunLimit <= 0 {
		o.cfg.RunLimit = defaultRunLimit
	}
	o.events, o.slotByThread, o.slotsOfUser = make(chan func()), map[string]*slot{}, map[string]int{}
	// What a start that fails part way began, the claim kept alive and runs it launched, ends before it returns.
	ctx, stop := context.WithCancel(ctx)
	fail := func(err error) error {
		stop()
		o.Wait()
		return err
	}
	o.ctx = ctx
	if o.cfg.Lease > 0 {
		var err error
		if o.ctx, err = o.claimShard(ctx); err != nil {
			return fail(err)
		}
	}
	if err := o.recover(); err != nil {
		return fail(err)
	}
	o.done.Add(1)
	go o.loop()
	models, _ := o.res.Models()
	slog.Info("orchestrator started", "name", o.cfg.Name, "slots", o.cfg.Slots, "models", len(models), "tools", len(o.res.Tools().Descriptors()))
	return nil
}

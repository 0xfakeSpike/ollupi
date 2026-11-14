package orchestrator

import (
	"context"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultSlots    = 32
	defaultRunLimit = 4 * time.Hour
	// defaultBackgroundCalls and defaultBackgroundLimit bound the calls one user runs in the background.
	defaultBackgroundCalls = 4
	defaultBackgroundLimit = 4 * time.Hour
)

// Config is what the orchestrator schedules and runs turns with.
type Config struct {
	DB *db.DB
	// Attachments keeps the files sent with messages, and Memory the users' memory; runs read them.
	Attachments blobs.Store
	Memory      *memory.Manager
	OnEvent     func(event.Event)
	// Runtime is what every run goes by from the deployment's configuration.
	Runtime runtime.Config
	Slots   int
	// UserSlots is the most slots one user may hold, so that one user's turns cannot fill a shard; by default, all.
	UserSlots int
	// RunLimit is how long a turn may run in its slot before it fails; by default, four hours.
	RunLimit time.Duration
	// BackgroundCalls is the most calls one user may run in the background at once; by default, four. A background
	// call holds no slot.
	BackgroundCalls int
	// BackgroundLimit is how long a background call may run before it fails; by default, four hours.
	BackgroundLimit time.Duration
	// Lease makes this process claim the shard before it runs it, waiting for as long as another holds it, and keep
	// the claim alive; a process that dies leaves its claim for this long. Without it the process trusts it is alone.
	Lease time.Duration
	// OnLeaseLost tells the host that another process took the shard: the orchestrator has stopped, and all else on DB must.
	OnLeaseLost func()
}

// An Orchestrator schedules the turns of a shard into slots and runs them, launches subagents and runs background
// calls. What clients ask of the shard is the service's; it tells the orchestrator what to look at.
type Orchestrator struct {
	cfg Config
	ctx context.Context
	res *resources.Resources
	ev  event.Emitter

	// backgroundCancels stop the background calls running on this process, by id.
	backgroundMu      sync.Mutex
	backgroundCancels map[string]context.CancelFunc

	events       chan func()
	slotByThread map[string]*slot
	slotsOfUser  map[string]int
	owner        string
	done         sync.WaitGroup
}

func New(cfg Config) *Orchestrator {
	return &Orchestrator{cfg: cfg, ev: event.Emitter{DB: cfg.DB, OnEvent: cfg.OnEvent}, backgroundCancels: map[string]context.CancelFunc{}}
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
	if o.cfg.BackgroundCalls <= 0 {
		o.cfg.BackgroundCalls = defaultBackgroundCalls
	}
	if o.cfg.BackgroundLimit <= 0 {
		o.cfg.BackgroundLimit = defaultBackgroundLimit
	}
	o.events, o.slotByThread, o.slotsOfUser = make(chan func()), map[string]*slot{}, map[string]int{}
	o.ctx = ctx
	if o.cfg.Lease > 0 {
		var err error
		if o.ctx, err = o.claimShard(ctx); err != nil {
			return err
		}
	}
	if _, err := o.cfg.DB.InterruptBackgroundCalls(time.Now()); err != nil {
		return fmt.Errorf("orchestrator: end the background calls of a process that stopped: %w", err)
	}
	if err := o.recover(); err != nil {
		return err
	}
	o.done.Add(1)
	go o.loop()
	undelivered, err := o.cfg.DB.UndeliveredBackgroundThreads()
	if err != nil {
		return fmt.Errorf("orchestrator: background results to deliver: %w", err)
	}
	for _, id := range undelivered {
		o.post(func() { o.deliverBackground(id) })
	}
	models, _ := o.res.Models()
	slog.Info("orchestrator started", "name", o.cfg.Runtime.Name, "slots", o.cfg.Slots, "models", len(models), "tools", len(o.res.Tools().Descriptors()))
	return nil
}

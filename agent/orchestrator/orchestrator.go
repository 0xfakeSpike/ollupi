package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

const (
	defaultSlots    = 32
	defaultRunLimit = 4 * time.Hour
)

type Orchestrator struct {
	store                   *db.DB
	attachments             blobs.Store
	memory                  *memory.Manager
	name, prompt, skillsDir string
	permissions             permission.Deployment
	slots, userSlots        int
	runLimit                time.Duration
	// Without a lease the process trusts it is alone; onLeaseLost tells the host the orchestrator has stopped, and all
	// else on the store must.
	lease       time.Duration
	onLeaseLost func()

	ctx context.Context
	res *resources.Resources
	ev  event.Emitter

	events       chan func()
	slotByThread map[string]*slot
	slotsOfUser  map[string]int
	owner        string
	done         sync.WaitGroup
}

func New(store *db.DB, attachments blobs.Store, memory *memory.Manager, onEvent func(event.Event),
	name, prompt, skillsDir string, permissions permission.Deployment,
	slots, userSlots int, runLimit, lease time.Duration, onLeaseLost func()) *Orchestrator {
	return &Orchestrator{
		store: store, attachments: attachments, memory: memory,
		name: name, prompt: prompt, skillsDir: skillsDir, permissions: permissions,
		slots: slots, userSlots: userSlots, runLimit: runLimit, lease: lease, onLeaseLost: onLeaseLost,
		ev: event.Emitter{DB: store, OnEvent: onEvent},
	}
}

func (o *Orchestrator) Start(ctx context.Context, res *resources.Resources) error {
	o.res = res
	if o.slots <= 0 {
		o.slots = defaultSlots
	}
	if o.userSlots <= 0 {
		o.userSlots = o.slots
	}
	if o.runLimit <= 0 {
		o.runLimit = defaultRunLimit
	}
	o.events, o.slotByThread, o.slotsOfUser = make(chan func()), map[string]*slot{}, map[string]int{}
	ctx, stop := context.WithCancel(ctx)
	fail := func(err error) error {
		stop()
		o.Wait()
		return err
	}
	o.ctx = ctx
	if o.lease > 0 {
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
	return nil
}

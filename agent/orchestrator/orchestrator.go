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

	ctx context.Context
	res *resources.Resources
	ev  *event.Bus

	queue        chan message
	slotByThread map[string]*slot
	slotsOfUser  map[string]int
	done         sync.WaitGroup
}

func New(store *db.DB, attachments blobs.Store, memory *memory.Manager, events *event.Bus,
	name, prompt, skillsDir string, permissions permission.Deployment,
	slots, userSlots int, runLimit time.Duration) *Orchestrator {
	return &Orchestrator{
		store: store, attachments: attachments, memory: memory,
		name: name, prompt: prompt, skillsDir: skillsDir, permissions: permissions,
		slots: slots, userSlots: userSlots, runLimit: runLimit,
		ev: events,
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
	o.queue, o.slotByThread, o.slotsOfUser = make(chan message), map[string]*slot{}, map[string]int{}
	ctx, stop := context.WithCancel(ctx)
	fail := func(err error) error {
		stop()
		o.Wait()
		return err
	}
	o.ctx = ctx
	if err := o.recover(); err != nil {
		return fail(err)
	}
	o.done.Add(1)
	go o.consume()
	return nil
}

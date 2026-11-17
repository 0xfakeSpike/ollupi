// Package role holds the roles of agent, side by side: the two main agents and the subagents, each defined in a file
// of its own; and the Runtime that runs a turn as the role it names. Each run, or its recovery after a crash, rebuilds
// that agent from the role and the turn's items alone: they are the run's state, and each of its steps is read from
// them.
package role

import (
	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

type Name string

var NameValues = []Name{FullMain, CompactMain, GeneralSub, BrowserSub}

type Role struct {
	name        Name
	rules       string
	answersUser bool
	own         []permission.Layer
	// Empty: every built-in tool.
	core                                                []string
	recalls                                             bool
	maxModelCalls, maxToolCalls, maxConsecutiveFailures int
}

var roles = map[Name]Role{FullMain: fullMain, CompactMain: compactMain, GeneralSub: generalSub, BrowserSub: browserSub}

func New(role Name, name, prompt, skillsDir string, permissions permission.Deployment, memory *memory.Manager, window *contextmanager.Manager,
	client llm.Client, store *db.DB, attachments blobs.Store, registry *tools.Registry, events event.Emitter) (*Runtime, bool) {
	r, ok := roles[role]
	if !ok {
		return nil, false
	}
	return &Runtime{
		role: r, agent: name, prompt: prompt, skillsDir: skillsDir, permissions: permissions, memory: memory, window: window,
		client: client, store: store, attachments: attachments, registry: registry, events: events,
	}, true
}

func ForMain(inputTokens int) Name {
	if inputTokens < compactBelow {
		return CompactMain
	}
	return FullMain
}

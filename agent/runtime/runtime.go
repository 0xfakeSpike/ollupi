package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
)

// The runtime traces through the global TracerProvider, as the agent does.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/agent/runtime")

const (
	maxModelCalls          = 200
	maxToolCalls           = 400
	maxConsecutiveFailures = 10
)

type Runtime struct {
	LLM      llm.Client
	Registry *tools.Registry
	Window   *contextmanager.Manager
	Host     Host

	Turn   repo.TurnRow
	Caller tools.Caller
	// Role is the role of agent the turn names, which the run is.
	Role        role.Role
	Prompt      string
	Permissions *permission.Checker
	// Settings are the settings of the turn's user, and Skills the skills the system prompt may list.
	Settings repo.SettingsRow
	Skills   []skills.Skill
	History  []repo.ItemRow
	Items    []repo.ItemRow
}

func (rt *Runtime) Run(ctx context.Context) (bool, error) {
	ctx = tools.WithCaller(ctx, rt.Caller)
	if err := rt.start(); err != nil {
		return false, err
	}
	return rt.run(ctx)
}

func (rt *Runtime) start() error {
	if rt.waiting() {
		return fmt.Errorf("runtime: turn %s still waits for an answer", rt.Turn.ID)
	}
	if !rt.Role.Memory || hasSnapshot(slices.Concat(rt.History, rt.Items)) {
		return nil
	}
	return rt.loadMemory()
}

func hasSnapshot(items []repo.ItemRow) bool {
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case it.Type == repo.ItemMemoryContext && it.Status == repo.ItemCompleted:
			return true
		case it.Type == repo.ItemContextCompaction && it.Status == repo.ItemCompleted:
			return false
		}
	}
	return false
}

func (rt *Runtime) loadMemory() error {
	snapshot, err := rt.Host.Memory(rt.Window.MemoryBudget())
	if err != nil {
		return fmt.Errorf("runtime: load memory: %w", err)
	}
	if snapshot == nil {
		return nil
	}
	now := time.Now().UTC()
	_, err = rt.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Turn.ID, Type: repo.ItemMemoryContext, Status: repo.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	})
	return err
}

// An offer is the role's split of the tools the run may use, as the run's items and permissions are at one point: the
// ones sent to the model, and the deferred ones it has to load first.
type offer struct {
	offered, deferred []tools.ToolDescriptor
}

func (rt *Runtime) offer() offer {
	offered, deferred := rt.Role.Offer(rt.Permissions.Tools(), rt.Window, slices.Concat(rt.History, rt.Items))
	return offer{offered: offered, deferred: deferred}
}

func (o offer) tool(name string) (tools.ToolDescriptor, bool) {
	i := slices.IndexFunc(o.offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return o.offered[i], true
}

// system is the role's system prompt for the offer.
func (rt *Runtime) system(of offer) string {
	return rt.Role.System(role.Input{
		Agent: rt.Prompt, Root: rt.Caller.Root,
		Skills: rt.Skills, SkillTokens: rt.Window.SkillBudget(), Deferred: of.deferred,
	})
}

func (rt *Runtime) run(ctx context.Context) (bool, error) {
	for {
		of := rt.offer()
		plan, err := rt.planCalls(ctx, of)
		if err != nil {
			return false, err
		}
		if final, err := rt.executePlan(ctx, of, plan); final || err != nil || rt.waiting() {
			return final, err
		}
		if rt.modelCalls() >= maxModelCalls {
			return true, rt.finish(ctx, fmt.Sprintf("reached the model call limit (%d)", maxModelCalls))
		}
		seen := 0
		if n := len(rt.Items); n > 0 {
			seen = rt.Items[n-1].Seq
		}
		resp, err := rt.respond(ctx, llm.ToolChoiceAuto)
		if err != nil {
			return false, err
		}
		items, permissions, settings, err := rt.Host.Reload()
		if err != nil {
			return false, fmt.Errorf("runtime: reload the turn: %w", err)
		}
		rt.Items, rt.Permissions, rt.Settings = items, permissions, settings
		if resp.FinishReason == "length" && len(resp.Message.ToolCalls) > 0 {
			if halt := rt.haltAfterFailures(cutOffCall); halt != "" {
				return true, rt.finish(ctx, halt)
			}
		}
		if len(resp.Message.ToolCalls) == 0 && !slices.ContainsFunc(rt.Items, func(it repo.ItemRow) bool {
			return (it.Type == repo.ItemUserMessage || it.Type == repo.ItemBackgroundResult) && it.Seq > seen
		}) {
			return true, nil
		}
	}
}

const finalAnswerNotice = `[runtime] Tool use has stopped: %s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`

func (rt *Runtime) finish(ctx context.Context, reason string) error {
	now := time.Now().UTC()
	if _, err := rt.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Turn.ID, Type: repo.ItemError, Status: repo.ItemCompleted,
		Text: fmt.Sprintf(finalAnswerNotice, reason), StartedAt: now, CompletedAt: &now,
	}); err != nil {
		return err
	}
	if _, err := rt.respond(ctx, llm.ToolChoiceNone); err != nil {
		return fmt.Errorf("%w (tool use stopped: %s)", err, reason)
	}
	return nil
}

func (rt *Runtime) record(it repo.ItemRow) (repo.ItemRow, error) {
	stored, err := rt.Host.Record(it)
	if err != nil {
		return repo.ItemRow{}, fmt.Errorf("runtime: record %s item: %w", it.Type, err)
	}
	for i := range rt.Items {
		if rt.Items[i].ID == stored.ID {
			rt.Items[i] = stored
			return stored, nil
		}
	}
	rt.Items = append(rt.Items, stored)
	return stored, nil
}

func (rt *Runtime) complete(it repo.ItemRow, status repo.ItemStatus) (repo.ItemRow, error) {
	now := time.Now().UTC()
	it.Status = status
	it.CompletedAt = &now
	return rt.record(it)
}

func withError(it repo.ItemRow, text string) repo.ItemRow {
	it.Error = text
	return it
}

func (rt *Runtime) waiting() bool {
	return slices.ContainsFunc(rt.Items, func(it repo.ItemRow) bool { return it.WaitsOnUser() || it.Awaits() })
}

package runtime

import (
	"context"
	"fmt"
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

// The runtime traces through the global TracerProvider, as the orchestrator does.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/agent/runtime")

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

// Run runs the turn until it ends with the final answer, which it reports, or waits. Each step is what Next decides
// from the run's state; Run carries it out and keeps what came of it.
func (rt *Runtime) Run(ctx context.Context) (bool, error) {
	ctx = tools.WithCaller(ctx, rt.Caller)
	if waiting(rt.Items) {
		return false, fmt.Errorf("runtime: turn %s still waits for an answer", rt.Turn.ID)
	}
	inspections, prompted, memoryOff := map[string]Inspection{}, 0, false
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		step, err := Next(State{
			Turn: rt.Turn, Role: rt.Role, Prompt: rt.Prompt, Root: rt.Caller.Root, Skills: rt.Skills,
			Window: rt.Window, Permissions: rt.Permissions, History: rt.History, Items: rt.Items,
			Inspections: inspections, Prompted: prompted, MemoryOff: memoryOff,
		})
		if err != nil {
			return false, err
		}
		switch st := step.(type) {
		case Wait:
			return false, nil
		case Done:
			return true, nil
		case LoadMemory:
			memoryOff, err = rt.loadMemory(st)
		case Inspect:
			inspections[st.Call.ID], err = rt.inspect(ctx, st)
		case Calls:
			err = rt.runCalls(ctx, st)
		case Stop:
			_, err = rt.record(st.Notice)
		case Compact:
			err = rt.compact(ctx, st)
		case CallModel:
			if err = rt.respond(ctx, st); err != nil {
				if st.Stopped != "" {
					err = fmt.Errorf("%w (tool use stopped: %s)", err, st.Stopped)
				}
				break
			}
			inspections, prompted = map[string]Inspection{}, st.Prompted
			items, permissions, settings, rerr := rt.Host.Reload()
			if rerr != nil {
				return false, fmt.Errorf("runtime: reload the turn: %w", rerr)
			}
			rt.Items, rt.Permissions, rt.Settings = items, permissions, settings
		}
		if err != nil {
			return false, err
		}
	}
}

// loadMemory records the user's memory snapshot, and reports whether there was none to load.
func (rt *Runtime) loadMemory(st LoadMemory) (bool, error) {
	snapshot, err := rt.Host.Memory(st.Budget)
	if err != nil {
		return false, fmt.Errorf("runtime: load memory: %w", err)
	}
	if snapshot == nil {
		return true, nil
	}
	now := time.Now().UTC()
	_, err = rt.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Turn.ID, Type: repo.ItemMemoryContext, Status: repo.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	})
	return false, err
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

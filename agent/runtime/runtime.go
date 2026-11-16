package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

const (
	maxModelCalls = 200
	maxToolCalls  = 400
	// maxParallelCalls bounds the read-only calls of one answer that run at once.
	maxParallelCalls       = 8
	maxConsecutiveFailures = 10
)

// A Runtime runs one turn. It is given where the data is kept and what its user runs on, and reads the rest itself:
// the turn, its conversation and workspace, the items, the user's settings and the permissions.
type Runtime struct {
	LLM llm.Client

	DB          *db.DB
	Attachments blobs.Store
	Registry    *tools.Registry
	// Events tells those who watch the thread of the items the run records and the text the model streams.
	Events event.Emitter

	// Role is the role the turn names, which Run loads the turn into and runs it as.
	Role role.Role
}

// Run runs the turn, which names Role, until it ends with the final answer, which it reports, or waits. It loads the
// turn into the role, which keeps the user's secrets with their key and takes in their memory, before the first
// model call.
func (rt *Runtime) Run(ctx context.Context, turnID string) (bool, db.Usage, error) {
	if err := rt.Role.Load(rt.DB, rt.Registry, turnID); err != nil {
		return false, db.Usage{}, fmt.Errorf("runtime: %w", err)
	}
	if rt.waiting() {
		return false, db.Usage{}, fmt.Errorf("runtime: turn %s still waits for an answer", rt.Role.Turn().ID)
	}
	settings, err := rt.DB.Settings(rt.Role.Thread().UserID)
	if err != nil {
		return false, db.Usage{}, fmt.Errorf("runtime: read the settings: %w", err)
	}
	rt.Role.Redact(settings.RedactionKey)
	if err := rt.loadMemory(); err != nil {
		return false, db.Usage{}, err
	}
	for {
		if final, err := rt.callTools(ctx); final || err != nil || rt.waiting() {
			return final, rt.Role.Usage(), err
		}
		if rt.modelCalls() >= maxModelCalls {
			return true, rt.Role.Usage(), rt.finish(ctx, fmt.Sprintf("reached the model call limit (%d)", maxModelCalls))
		}
		seen := 0
		if n := len(rt.Role.Items()); n > 0 {
			seen = rt.Role.Items()[n-1].Seq
		}
		resp, err := rt.respond(ctx, llm.ToolChoiceAuto)
		if err != nil {
			return false, rt.Role.Usage(), err
		}
		if err := rt.Role.Reload(rt.DB, rt.Registry); err != nil {
			return false, rt.Role.Usage(), err
		}
		if resp.FinishReason == "length" && len(resp.Message.ToolCalls) > 0 {
			if halt := rt.haltAfterFailures(cutOffCall); halt != "" {
				return true, rt.Role.Usage(), rt.finish(ctx, halt)
			}
		}
		// The answer is final unless it called tools or input came while it streamed.
		if len(resp.Message.ToolCalls) == 0 && !slices.ContainsFunc(rt.Role.Items(), func(it db.ItemRow) bool {
			return (it.Type == db.ItemUserMessage || it.Type == db.ItemAgentReport) && it.Seq > seen
		}) {
			return true, rt.Role.Usage(), nil
		}
	}
}

// loadMemory records the memory snapshot the role recalls now, if any.
func (rt *Runtime) loadMemory() error {
	it, err := rt.Role.Recall()
	if err != nil {
		return fmt.Errorf("runtime: load memory: %w", err)
	}
	if it == nil {
		return nil
	}
	_, err = rt.record(*it)
	return err
}

const finalAnswerNotice = `[runtime] Tool use has stopped: %s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`

func (rt *Runtime) finish(ctx context.Context, reason string) error {
	now := time.Now().UTC()
	if _, err := rt.record(db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.Role.Turn().ID, Type: db.ItemError, Status: db.ItemCompleted,
		Text: fmt.Sprintf(finalAnswerNotice, reason), StartedAt: now, CompletedAt: &now,
	}); err != nil {
		return err
	}
	if _, err := rt.respond(ctx, llm.ToolChoiceNone); err != nil {
		return fmt.Errorf("%w (tool use stopped: %s)", err, reason)
	}
	return nil
}

func (rt *Runtime) record(it db.ItemRow) (db.ItemRow, error) {
	stored, isNew, err := rt.DB.RecordItem(rt.Role.Thread().ID, it)
	if err != nil {
		return db.ItemRow{}, fmt.Errorf("runtime: record %s item: %w", it.Type, err)
	}
	rt.Events.Item(rt.Role.Thread(), rt.Role.Turn(), stored, isNew)
	rt.Role.Keep(stored)
	return stored, nil
}

func (rt *Runtime) complete(it db.ItemRow, status db.ItemStatus) (db.ItemRow, error) {
	now := time.Now().UTC()
	it.Status = status
	it.CompletedAt = &now
	return rt.record(it)
}

func withError(it db.ItemRow, text string) db.ItemRow {
	it.Error = text
	return it
}

func (rt *Runtime) waiting() bool {
	return slices.ContainsFunc(rt.Role.Items(), func(it db.ItemRow) bool { return it.WaitsOnUser() || it.Awaits() })
}

// caller is who makes the call that the item records: the run's role, in its conversation.
func (rt *Runtime) caller(it db.ItemRow) tools.Caller {
	t := rt.Role.Thread()
	_, deferred := rt.Role.Offer()
	names := make([]string, len(deferred))
	for i, d := range deferred {
		names[i] = d.Name
	}
	return tools.Caller{
		UserID: t.UserID, ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), EnvironmentID: string(t.EnvironmentID),
		Root: rt.Role.Workspace().RootPath, Subagent: t.ParentThreadID != "", Role: string(rt.Role.Name()), Item: it.ID,
		Deferred: names,
	}
}

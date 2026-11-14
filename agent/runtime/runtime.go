package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
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

// Config is what every run goes by from the deployment's configuration.
type Config struct {
	// Name and Prompt make the agent's own prompt, and SkillsDir is where each role's skills are installed.
	Name, Prompt, SkillsDir string
	// Settings are the default permission settings, which serve users with different tools when Shared. Tools, when
	// set, are the only tools a turn may use.
	Settings permission.Settings
	Shared   bool
	Tools    []string
}

// A Runtime runs one turn. It is given where the data is kept and what its user runs on, and reads the rest itself:
// the turn, its conversation and workspace, the items, the user's settings and the permissions.
type Runtime struct {
	DB *db.DB
	// Attachments keeps the files sent with messages; without it messages have none.
	Attachments blobs.Store
	LLM         llm.Client
	Registry    *tools.Registry
	Window      *contextmanager.Manager
	Host        Host
	Config      Config
	// TurnID is the turn to run.
	TurnID string

	turn db.TurnRow
	// thread is the turn's conversation, and workspace the workspace it works in, if any.
	thread    db.ThreadRow
	workspace db.WorkspaceRow
	// role is the role of agent the turn names, which the run is.
	role        role.Role
	permissions *permission.Checker
	history     []db.ItemRow
	items       []db.ItemRow

	offered  []tools.ToolDescriptor
	deferred []tools.ToolDescriptor
	redactor *redact.Redactor
	usage    db.Usage
}

func (rt *Runtime) Run(ctx context.Context) (bool, db.Usage, error) {
	if err := rt.load(); err != nil {
		return false, db.Usage{}, err
	}
	if err := rt.start(); err != nil {
		return false, db.Usage{}, err
	}
	final, err := rt.run(ctx)
	return final, rt.usage, err
}

// load reads the turn, its conversation and the items of the conversation's turns, and makes the permissions of the
// turn's role.
func (rt *Runtime) load() error {
	turn, err := rt.DB.Turn(rt.TurnID)
	if err != nil {
		return fmt.Errorf("runtime: turn %s: %w", rt.TurnID, err)
	}
	r, ok := role.Of(role.Name(turn.Agent))
	if !ok {
		return fmt.Errorf("runtime: turn %s names role %q, which is none of %q", turn.ID, turn.Agent, role.NameValues)
	}
	thread, err := rt.DB.ThreadRow(turn.ThreadID)
	if err != nil {
		return fmt.Errorf("runtime: thread %s: %w", turn.ThreadID, err)
	}
	items, err := rt.DB.RunItems(thread.ID)
	if err != nil {
		return fmt.Errorf("runtime: the items of thread %s: %w", thread.ID, err)
	}
	rt.turn, rt.role = turn, r
	for _, it := range items {
		if it.TurnID == turn.ID {
			rt.items = append(rt.items, it)
		} else {
			rt.history = append(rt.history, it)
		}
	}
	return rt.permit(thread)
}

// permit makes the permissions of the turn's role in the conversation as it is now, with its workspace's: the role's
// own layer, below the default, workspace and conversation settings, with the tools the configuration allows.
func (rt *Runtime) permit(t db.ThreadRow) error {
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = rt.DB.Workspace(string(t.WorkspaceID)); err != nil {
			return fmt.Errorf("runtime: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	// The role's layer comes first, so that the user's settings decide over it.
	layers := append([]permission.Layer{rt.role.Permission}, permission.Base(rt.Config.Settings, rt.Config.Shared, w)...)
	if t.ParentThreadID == "" {
		layers = append(layers, permission.ThreadLayer(t.Settings))
	} else {
		parent, err := rt.DB.ThreadRow(string(t.ParentThreadID))
		if err != nil {
			return fmt.Errorf("runtime: parent thread %s: %w", t.ParentThreadID, err)
		}
		layers = append(layers, permission.ParentLayer(parent.Settings))
	}
	var allowlist []string
	if rt.Config.Tools != nil {
		allowlist = []string{}
		for _, d := range rt.Registry.Descriptors() {
			if slices.Contains(rt.Config.Tools, d.Name) {
				allowlist = append(allowlist, d.Name)
			}
		}
	}
	checker, err := permission.New(layers, allowlist, rt.Registry)
	if err != nil {
		return err
	}
	rt.thread, rt.workspace, rt.permissions = t, w, checker
	return nil
}

func (rt *Runtime) start() error {
	if rt.waiting() {
		return fmt.Errorf("runtime: turn %s still waits for an answer", rt.turn.ID)
	}
	settings, err := rt.DB.Settings(rt.thread.UserID)
	if err != nil {
		return fmt.Errorf("runtime: read the settings: %w", err)
	}
	rt.redactor = redact.New(settings.RedactionKey)
	rt.offer()
	if !rt.role.Memory || hasSnapshot(slices.Concat(rt.history, rt.items)) {
		return nil
	}
	return rt.loadMemory()
}

func hasSnapshot(items []db.ItemRow) bool {
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case it.Type == db.ItemMemoryContext && it.Status == db.ItemCompleted:
			return true
		case it.Type == db.ItemContextCompaction && it.Status == db.ItemCompleted:
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
	_, err = rt.record(db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemMemoryContext, Status: db.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	})
	return err
}

func (rt *Runtime) tool(name string) (tools.ToolDescriptor, bool) {
	i := slices.IndexFunc(rt.offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return rt.offered[i], true
}

// offer keeps the role's split of the tools the run may use.
func (rt *Runtime) offer() {
	rt.offered, rt.deferred = rt.role.Offer(rt.permissions.Tools(), rt.Window, slices.Concat(rt.history, rt.items))
}

func (rt *Runtime) run(ctx context.Context) (bool, error) {
	for {
		plan, err := rt.planCalls(ctx)
		if err != nil {
			return false, err
		}
		if final, err := rt.executePlan(ctx, plan); final || err != nil || rt.waiting() {
			return final, err
		}
		if rt.modelCalls() >= maxModelCalls {
			return true, rt.finish(ctx, fmt.Sprintf("reached the model call limit (%d)", maxModelCalls))
		}
		seen := 0
		if n := len(rt.items); n > 0 {
			seen = rt.items[n-1].Seq
		}
		resp, err := rt.respond(ctx, llm.ToolChoiceAuto)
		if err != nil {
			return false, err
		}
		if err := rt.reload(); err != nil {
			return false, err
		}
		rt.offer()
		if resp.FinishReason == "length" && len(resp.Message.ToolCalls) > 0 {
			if halt := rt.haltAfterFailures(cutOffCall); halt != "" {
				return true, rt.finish(ctx, halt)
			}
		}
		if len(resp.Message.ToolCalls) == 0 && !slices.ContainsFunc(rt.items, func(it db.ItemRow) bool {
			return (it.Type == db.ItemUserMessage || it.Type == db.ItemBackgroundResult) && it.Seq > seen
		}) {
			return true, nil
		}
	}
}

const finalAnswerNotice = `[runtime] Tool use has stopped: %s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`

func (rt *Runtime) finish(ctx context.Context, reason string) error {
	now := time.Now().UTC()
	if _, err := rt.record(db.ItemRow{
		ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemError, Status: db.ItemCompleted,
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
	stored, isNew, err := rt.DB.RecordItem(rt.thread.ID, it)
	if err != nil {
		return db.ItemRow{}, fmt.Errorf("runtime: record %s item: %w", it.Type, err)
	}
	rt.Host.Notify(stored, isNew)
	for i := range rt.items {
		if rt.items[i].ID == stored.ID {
			rt.items[i] = stored
			return stored, nil
		}
	}
	rt.items = append(rt.items, stored)
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
	return slices.ContainsFunc(rt.items, func(it db.ItemRow) bool { return it.WaitsOnUser() || it.Awaits() })
}

// caller is who makes the call that the item records: the run's role, in its conversation.
func (rt *Runtime) caller(it db.ItemRow) tools.Caller {
	t := rt.thread
	return tools.Caller{
		UserID: t.UserID, ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), EnvironmentID: string(t.EnvironmentID),
		Root: rt.workspace.RootPath, Subagent: t.ParentThreadID != "", Role: string(rt.role.Name), Item: it.ID,
	}
}

// reload reads the turn's items again, with the permissions of its conversation as they are now.
func (rt *Runtime) reload() error {
	items, err := rt.DB.TurnItems(rt.turn.ID)
	if err != nil {
		return fmt.Errorf("runtime: reload the turn: %w", err)
	}
	current, err := rt.DB.ThreadRow(rt.thread.ID)
	if err != nil {
		return fmt.Errorf("runtime: thread %s: %w", rt.thread.ID, err)
	}
	rt.items = items
	return rt.permit(current)
}

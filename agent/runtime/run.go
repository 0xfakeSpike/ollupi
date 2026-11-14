package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/blobs"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
)

// The runtime traces through the global TracerProvider, as the orchestrator does.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/agent/runtime")

// Config is what every run of a turn goes by.
type Config struct {
	DB *repo.DB
	// Name and Prompt make the agent's own prompt; see role.AgentPrompt.
	Name, Prompt string
	// Settings are the default permission settings, which serve users with different tools when Shared. Tools, when
	// set, are the only tools a turn may use.
	Settings permission.Settings
	Shared   bool
	Tools    []string
	// Skills, Memory and Attachments may be nil: then no skill is listed, no memory is loaded and no message has
	// attachments.
	Skills      *skills.Catalog
	Memory      *memory.Manager
	Attachments blobs.Store
	// BackgroundCalls is the most background calls a user has at once.
	BackgroundCalls int
	// Emit tells those who watch a thread what happened to it.
	Emit func(event.Event)
	// Queued says a background call was queued, for whoever starts them.
	Queued func()
}

// A Turn is a turn to run, with what its user runs it on.
type Turn struct {
	Thread repo.ThreadRow
	Turn   repo.TurnRow
	// Items are the thread's items a run reads: the turn's and those of the turns before it.
	Items    []repo.ItemRow
	Client   llm.Client
	Window   *contextmanager.Manager
	Registry *tools.Registry
}

// Run runs the turn until it ends with the final answer, which it reports, or waits. Each step is what Next decides
// from the run's state; Run carries it out, records what came of it and keeps the state for the next step. Nothing
// stays of a run but what it recorded: the next run of the turn starts again from its items.
func Run(ctx context.Context, cfg Config, turn Turn) (bool, error) {
	r, ok := role.Of(role.Name(turn.Turn.Agent))
	if !ok {
		return false, fmt.Errorf("runtime: turn %s names role %q, which is none of %q", turn.Turn.ID, turn.Turn.Agent, role.NameValues)
	}
	checker, w, err := permissions(cfg, turn.Thread, r, turn.Registry)
	if err != nil {
		return false, err
	}
	settings, err := cfg.DB.Settings(turn.Thread.UserID)
	if err != nil {
		return false, err
	}
	var catalog []skills.Skill
	if cfg.Skills != nil {
		catalog = cfg.Skills.All()
	}
	var history, items []repo.ItemRow
	for _, it := range turn.Items {
		if it.TurnID == turn.Turn.ID {
			items = append(items, it)
		} else {
			history = append(history, it)
		}
	}
	t := turn.Thread
	ru := &run{
		cfg: cfg, t: t, client: turn.Client, registry: turn.Registry, settings: settings,
		caller: tools.Caller{
			UserID: t.UserID, ThreadID: t.ID, WorkspaceID: string(t.WorkspaceID), EnvironmentID: string(t.EnvironmentID),
			Root: w.RootPath, Subagent: t.ParentThreadID != "",
		},
		state: State{
			Turn: turn.Turn, Role: r, Prompt: role.AgentPrompt(cfg.Name, cfg.Prompt), Root: w.RootPath, Skills: catalog,
			Window: turn.Window, Permissions: checker, History: history, Items: items, Inspections: map[string]Inspection{},
		},
	}
	return ru.run(ctx)
}

// permissions are what the thread's turns may do as role r: the role's own layer, below the default, workspace and
// conversation settings, with the tools the configuration allows. They come with the thread's workspace.
func permissions(cfg Config, t repo.ThreadRow, r role.Role, registry *tools.Registry) (*permission.Checker, repo.WorkspaceRow, error) {
	var w repo.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = cfg.DB.Workspace(string(t.WorkspaceID)); err != nil {
			return nil, repo.WorkspaceRow{}, fmt.Errorf("runtime: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	// The role's layer comes first, so that the user's settings decide over it.
	layers := append([]permission.Layer{r.Permission}, permission.Base(cfg.Settings, cfg.Shared, w)...)
	if t.ParentThreadID == "" {
		layers = append(layers, permission.ThreadLayer(t.Settings))
	} else {
		parent, err := cfg.DB.ThreadRow(string(t.ParentThreadID))
		if err != nil {
			return nil, repo.WorkspaceRow{}, fmt.Errorf("runtime: parent thread %s: %w", t.ParentThreadID, err)
		}
		layers = append(layers, permission.ParentLayer(parent.Settings))
	}
	var allowlist []string
	if cfg.Tools != nil {
		allowlist = []string{}
		for _, d := range registry.Descriptors() {
			if slices.Contains(cfg.Tools, d.Name) {
				allowlist = append(allowlist, d.Name)
			}
		}
	}
	checker, err := permission.New(layers, allowlist, registry)
	if err != nil {
		return nil, repo.WorkspaceRow{}, err
	}
	return checker, w, nil
}

// A run is one run of a turn as it goes: its state, and what it runs the turn on.
type run struct {
	cfg      Config
	t        repo.ThreadRow
	caller   tools.Caller
	registry *tools.Registry
	client   llm.Client
	settings repo.SettingsRow
	state    State
}

func (r *run) run(ctx context.Context) (bool, error) {
	ctx = tools.WithCaller(ctx, r.caller)
	if waiting(r.state.Items) {
		return false, fmt.Errorf("runtime: turn %s still waits for an answer", r.state.Turn.ID)
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		step, err := Next(r.state)
		if err != nil {
			return false, err
		}
		switch st := step.(type) {
		case Wait:
			return false, nil
		case Done:
			return true, nil
		case LoadMemory:
			err = r.loadMemory(st)
		case Inspect:
			r.state.Inspections[st.Call.ID], err = r.inspect(ctx, st)
		case Calls:
			err = r.runCalls(ctx, st)
		case Stop:
			_, err = r.record(st.Notice)
		case Compact:
			err = r.compact(ctx, st)
		case CallModel:
			if err = r.respond(ctx, st); err != nil {
				if st.Stopped != "" {
					err = fmt.Errorf("%w (tool use stopped: %s)", err, st.Stopped)
				}
				break
			}
			r.state.Inspections, r.state.Prompted = map[string]Inspection{}, st.Prompted
			err = r.reload()
		}
		if err != nil {
			return false, err
		}
	}
}

// record stores an item of the turn, tells those who watch the thread, and keeps it in the run's state.
func (r *run) record(it repo.ItemRow) (repo.ItemRow, error) {
	stored, isNew, err := r.cfg.DB.RecordItem(r.t.ID, it)
	if err != nil {
		return repo.ItemRow{}, fmt.Errorf("runtime: record %s item: %w", it.Type, err)
	}
	for _, e := range event.OfItem(r.t, r.state.Turn, stored, isNew) {
		r.cfg.Emit(e)
	}
	items := r.state.Items
	if i := slices.IndexFunc(items, func(row repo.ItemRow) bool { return row.ID == stored.ID }); i >= 0 {
		items[i] = stored
	} else {
		r.state.Items = append(items, stored)
	}
	return stored, nil
}

// reload reads the turn's items again, with the permissions of its thread and the settings of its user as they are
// now.
func (r *run) reload() error {
	items, err := r.cfg.DB.TurnItems(r.state.Turn.ID)
	if err != nil {
		return fmt.Errorf("runtime: reload the turn: %w", err)
	}
	current, err := r.cfg.DB.ThreadRow(r.t.ID)
	if err != nil {
		return fmt.Errorf("runtime: thread %s: %w", r.t.ID, err)
	}
	checker, _, err := permissions(r.cfg, current, r.state.Role, r.registry)
	if err != nil {
		return err
	}
	settings, err := r.cfg.DB.Settings(r.t.UserID)
	if err != nil {
		return err
	}
	r.state.Items, r.state.Permissions, r.settings = items, checker, settings
	return nil
}

// loadMemory records the user's memory snapshot; a configuration that keeps no memory leaves the run without.
func (r *run) loadMemory(st LoadMemory) error {
	if r.cfg.Memory == nil {
		r.state.MemoryOff = true
		return nil
	}
	snapshot, err := r.cfg.Memory.Snapshot(r.t.UserID, string(r.t.WorkspaceID), st.Budget)
	if err != nil {
		return fmt.Errorf("runtime: load memory: %w", err)
	}
	if snapshot == nil {
		r.state.MemoryOff = true
		return nil
	}
	now := time.Now().UTC()
	_, err = r.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: r.state.Turn.ID, Type: repo.ItemMemoryContext, Status: repo.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	})
	return err
}

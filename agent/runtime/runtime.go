package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/agent/systemprompt"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

const (
	maxModelCalls          = 200
	maxToolCalls           = 400
	maxConsecutiveFailures = 10
)

type Runtime struct {
	LLM      llm.Client
	Registry *tools.Registry
	Skills   *skills.Catalog
	Window   *contextmanager.Manager
	Memory   *memory.Manager

	Turn        repo.TurnRow
	Caller      tools.Caller
	Prompt      string
	Permissions *permission.Checker
	History     []repo.ItemRow
	Items       []repo.ItemRow
	Record      func(repo.ItemRow) (repo.ItemRow, error)
	Delta       func(repo.ItemRow, string)
	Reload      func() ([]repo.ItemRow, *permission.Checker, error)
	Settings    func() (repo.SettingsRow, error)

	AttachmentsDir string

	offered  []tools.ToolDescriptor
	deferred []tools.ToolDescriptor
	system   string
	redactor *redact.Redactor
	usage    repo.Usage
}

func (rt *Runtime) Run(ctx context.Context) (bool, repo.Usage, error) {
	ctx = tools.WithCaller(ctx, rt.Caller)
	if err := rt.start(); err != nil {
		return false, repo.Usage{}, err
	}
	final, err := rt.run(ctx)
	return final, rt.usage, err
}

func (rt *Runtime) start() error {
	if rt.waiting() {
		return fmt.Errorf("runtime: turn %s still waits for an answer", rt.Turn.ID)
	}
	settings, err := rt.Settings()
	if err != nil {
		return fmt.Errorf("runtime: read the settings: %w", err)
	}
	rt.redactor = redact.New(settings.RedactionKey)
	var catalog []skills.Skill
	if rt.Skills != nil {
		catalog = rt.Skills.All()
	}
	rt.offered, rt.deferred = Offer(rt.Permissions.Tools(), rt.Window, slices.Concat(rt.History, rt.Items))
	rt.system = systemprompt.Build(systemprompt.Input{
		Agent: rt.Prompt, Subagent: rt.Caller.Subagent, Root: rt.Caller.Root,
		Skills: catalog, SkillTokens: rt.Window.SkillBudget(), Deferred: rt.deferred,
	})
	if hasSnapshot(slices.Concat(rt.History, rt.Items)) {
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
	if rt.Memory == nil {
		return nil
	}
	snapshot, err := rt.Memory.Snapshot(rt.Caller.WorkspaceID, rt.Window.MemoryBudget())
	if err != nil {
		log.Printf("memory: turn %s goes on without a memory snapshot: %v", rt.Turn.ID, err)
		return nil
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

func (rt *Runtime) tool(name string) (tools.ToolDescriptor, bool) {
	i := slices.IndexFunc(rt.offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return rt.offered[i], true
}

// Offer splits the visible tools into the ones sent to the model and the ones only named in DEFERRED TOOLS.
// Tools of external providers are deferred when all visible tools would take more than the tool budget;
// the ones a load_tools call loaded follow the others in load order, so the tool list only grows.
func Offer(visible []tools.ToolDescriptor, window *contextmanager.Manager, items []repo.ItemRow) (offered, deferred []tools.ToolDescriptor) {
	external := func(d tools.ToolDescriptor) bool { return strings.Contains(d.Name, tools.ProviderSeparator) }
	loader := func(d tools.ToolDescriptor) bool { return d.Name == tools.ToolLoadTools }
	deferring := slices.ContainsFunc(visible, loader) && slices.ContainsFunc(visible, external) && !window.ToolsFit(Specs(visible))
	for _, d := range visible {
		switch {
		case deferring && external(d):
			deferred = append(deferred, d)
		case deferring || !loader(d):
			offered = append(offered, d)
		}
	}
	for _, name := range loadedTools(items) {
		i := slices.IndexFunc(deferred, func(d tools.ToolDescriptor) bool { return d.Name == name })
		if i >= 0 && !slices.ContainsFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name }) {
			offered = append(offered, deferred[i])
		}
	}
	return offered, deferred
}

func loadedTools(items []repo.ItemRow) []string {
	var names []string
	for _, it := range items {
		if it.Tool != tools.ToolLoadTools || it.Status != repo.ItemCompleted || it.Result == nil {
			continue
		}
		var out struct {
			Loaded []string `json:"loaded"`
		}
		if json.Unmarshal(it.Result.Output, &out) == nil {
			names = append(names, out.Loaded...)
		}
	}
	return names
}

func Specs(descriptors []tools.ToolDescriptor) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(descriptors))
	for _, d := range descriptors {
		specs = append(specs, llm.ToolSpec{Name: d.Name, Description: strings.TrimSpace(d.Description), Parameters: d.InputSchema})
	}
	return specs
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
		if n := len(rt.Items); n > 0 {
			seen = rt.Items[n-1].Seq
		}
		resp, err := rt.respond(ctx, llm.ToolChoiceAuto)
		if err != nil {
			return false, err
		}
		items, permissions, err := rt.Reload()
		if err != nil {
			return false, fmt.Errorf("runtime: reload the turn: %w", err)
		}
		rt.Items, rt.Permissions = items, permissions
		rt.offered, rt.deferred = Offer(permissions.Tools(), rt.Window, slices.Concat(rt.History, rt.Items))
		if len(resp.Message.ToolCalls) == 0 && !slices.ContainsFunc(rt.Items, func(it repo.ItemRow) bool { return it.Type == repo.ItemUserMessage && it.Seq > seen }) {
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
	stored, err := rt.Record(it)
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

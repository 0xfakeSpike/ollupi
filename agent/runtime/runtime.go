package runtime

import (
	"context"
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
	LLM      *llm.OpenAI
	Registry *tools.Registry
	Skills   *skills.Catalog
	Contexts *contextmanager.Manager
	Memory   *memory.Manager

	Turn        repo.TurnRow
	Caller      tools.Caller
	Prompt      string
	Tools       []tools.ToolDescriptor
	Permissions *permission.Checker
	History     []repo.ItemRow
	Items       []repo.ItemRow
	Record      func(repo.ItemRow) (repo.ItemRow, error)
	Delta       func(repo.ItemRow, string)
	Reload      func() ([]repo.ItemRow, *permission.Checker, error)
	// Redaction reports whether sensitive values are masked; every model
	// call asks, so the user's switch applies from the next one.
	Redaction func() (bool, error)

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
	rt.redactor = redact.New()
	var catalog []skills.Skill
	if rt.Skills != nil {
		catalog = rt.Skills.All()
	}
	rt.system = systemprompt.Build(systemprompt.Input{
		Agent: rt.Prompt, Subagent: rt.Caller.Subagent, Root: rt.Caller.Root,
		Skills: catalog, SkillTokens: rt.Contexts.SkillBudget(),
	})
	if hasSnapshot(slices.Concat(rt.History, rt.Items)) {
		return nil
	}
	return rt.loadMemory()
}

// hasSnapshot reports whether the thread has its memory snapshot: one
// recorded since its latest compaction, or when it started.
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

// loadMemory records the memory snapshot the thread works with until its
// next compaction, even an empty one, so memories learned meanwhile wait
// for that compaction instead of changing the start of every prompt.
func (rt *Runtime) loadMemory() error {
	if rt.Memory == nil {
		return nil
	}
	snapshot, err := rt.Memory.Snapshot(rt.Caller.WorkspaceID, rt.Contexts.MemoryBudget())
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
	i := slices.IndexFunc(rt.Tools, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return rt.Tools[i], true
}

func (rt *Runtime) specs() []llm.ToolSpec { return Specs(rt.Tools) }

func Specs(descriptors []tools.ToolDescriptor) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(descriptors))
	for _, d := range descriptors {
		specs = append(specs, llm.ToolSpec{Name: d.Name, Description: strings.TrimSpace(d.Description), Parameters: d.InputSchema})
	}
	return specs
}

func (rt *Runtime) run(ctx context.Context) (bool, error) {
	for {
		if final, err := rt.runCalls(ctx); final || err != nil || rt.waiting() {
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
		rt.Items, rt.Permissions, rt.Tools = items, permissions, permissions.Tools()
		if len(resp.Message.ToolCalls) == 0 && !slices.ContainsFunc(rt.Items, func(it repo.ItemRow) bool { return it.Type == repo.ItemUserMessage && it.Seq > seen }) {
			return true, nil
		}
	}
}

func (rt *Runtime) runCalls(ctx context.Context) (bool, error) {
	plan, err := rt.planCalls(ctx)
	if err != nil {
		return false, err
	}
	return rt.executePlan(ctx, plan)
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
	return slices.ContainsFunc(rt.Items, func(it repo.ItemRow) bool { return it.IsPending() || it.Awaits() })
}

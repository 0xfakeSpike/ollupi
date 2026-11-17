// Package agent is what an agent does next. A turn's state is its conversation's log, so the next action is read from
// the log alone, by Next, whether the turn runs for the first time or again after a crash. A role is what kind of
// agent runs the turn: its rules, the tools it keeps, its budget. The package does no I/O; a runner does what Next
// says and appends what came of it to the log.
package agent

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
)

type Name string

const (
	FullMain    Name = "full-main-agent"
	CompactMain Name = "compact-main-agent"
	GeneralSub  Name = "general-sub-agent"
	BrowserSub  Name = "browser-sub-agent"
)

var Names = []Name{FullMain, CompactMain, GeneralSub, BrowserSub}

// The built-in tools and providers the roles name; the tools take their names from here.
const (
	ReadFileTool       = "read_file"
	RunCommandTool     = "run_command"
	PlanTool           = "update_plan"
	ReadOutputTool     = "read_tool_output"
	LoadToolsTool      = "load_tools"
	BrowserProvider    = "browser"
	OutputsProvider    = "context"
	SkillsProvider     = "skills"
	PlanProvider       = "plan"
	SubagentsProvider  = "subagents"
	CronProvider       = "cron"
	WorkspaceProvider  = "workspace"
	compactBelowTokens = 16_384
)

type Role struct {
	Name  Name
	Rules string
	// A main agent answers the user; a subagent reports to the agent that launched it.
	AnswersUser bool
	// Own layers sit below every other, which decide over them.
	Own []policy.Layer
	// Core tools are offered first when not all fit; none means every built-in tool.
	Core                   []string
	Recalls                bool
	MaxModelCalls          int
	MaxToolCalls           int
	MaxConsecutiveFailures int
}

var roles = map[Name]Role{
	FullMain: {Name: FullMain, Rules: fullMainRules, AnswersUser: true, Recalls: true,
		MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10},
	CompactMain: {Name: CompactMain, Rules: compactMainRules, AnswersUser: true, Recalls: true, Core: []string{ReadFileTool, RunCommandTool},
		MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10},
	// Launching subagents or scheduling tasks would come back to the thread after the subagent reported.
	GeneralSub: {Name: GeneralSub, Rules: generalSubRules, Recalls: true,
		Own:           []policy.Layer{{Of: "the general subagent's role", Without: []string{SubagentsProvider, CronProvider}}},
		MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10},
	BrowserSub: {Name: BrowserSub, Rules: browserSubRules,
		Own:           []policy.Layer{{Of: "the browser subagent's role", Providers: []string{BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider}}},
		MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10},
}

func RoleOf(name Name) (Role, bool) {
	r, ok := roles[name]
	return r, ok
}

// Main is the main agent for a model's context window: about 8k tokens are what the full one's prompt and tools need,
// which the window holds to half of it.
func Main(inputTokens int) Name {
	if inputTokens < compactBelowTokens {
		return CompactMain
	}
	return FullMain
}

type Action interface{ action() }

// Wait yields the turn: it waits on the user, or on its subagents.
type Wait struct{}

// RunTools runs the calls the model made that may run now.
type RunTools struct{ Calls []conversation.Item }

// CallModel asks the model for its next response. A notice is appended first, to tell the model why tool use
// stopped; then, and until it answers, it may call no tool.
type CallModel struct {
	NoTools bool
	Notice  *conversation.Notice
}

type Finish struct{}

func (Wait) action()      {}
func (RunTools) action()  {}
func (CallModel) action() {}
func (Finish) action()    {}

// Next reads the turn in progress of v: what it waits on comes first, then calls that may run, then whether it has
// answered, then whether its budget or a declined call stops tool use.
func Next(v conversation.View, r Role) Action {
	turn, ok := v.Active()
	if !ok || v.Waiting() != conversation.WaitNone {
		return Wait{}
	}
	items := v.ItemsOf(turn.ID)
	var runnable []conversation.Item
	for _, it := range items {
		if it.Kind == conversation.ItemToolCall && it.Status == conversation.Pending {
			runnable = append(runnable, it)
		}
	}
	switch {
	case len(runnable) > 0:
		return RunTools{Calls: runnable}
	case v.Answered(turn.ID):
		return Finish{}
	}
	from := fresh(items)
	told := slices.ContainsFunc(items[from:], func(it conversation.Item) bool { return it.Kind == conversation.ItemNotice })
	if told {
		return CallModel{NoTools: true}
	}
	if reason := stop(v, turn.ID, items, r); reason != "" {
		return CallModel{NoTools: true, Notice: &conversation.Notice{Text: fmt.Sprintf(concludeNotice, reason)}}
	}
	return CallModel{}
}

func stop(v conversation.View, turn string, items []conversation.Item, r Role) string {
	from := fresh(items)
	if i := slices.IndexFunc(items[from:], declined); i >= 0 {
		return fmt.Sprintf("the user declined %s", items[from+i].Call.Tool)
	}
	calls := 0
	for _, it := range items {
		if it.Kind == conversation.ItemToolCall {
			calls++
		}
	}
	if calls >= r.MaxToolCalls {
		return fmt.Sprintf("reached the tool call limit (%d)", r.MaxToolCalls)
	}
	failed, last := 0, ""
	for i := len(items) - 1; i >= from; i-- {
		it := items[i]
		if it.Kind != conversation.ItemToolCall {
			continue
		}
		if it.Status != conversation.Errored {
			break
		}
		if failed == 0 {
			last = it.Error
		}
		failed++
	}
	if failed >= r.MaxConsecutiveFailures {
		return fmt.Sprintf("%d consecutive invalid or failed tool calls; last: %s", failed, last)
	}
	if responses(v, turn) >= r.MaxModelCalls {
		return fmt.Sprintf("reached the model call limit (%d)", r.MaxModelCalls)
	}
	return ""
}

func declined(it conversation.Item) bool {
	return it.Kind == conversation.ItemToolCall && it.Status == conversation.Declined
}

// fresh is where the input the agent works on now starts: after the latest message from the user, or after the
// latest answer, a response that called no tool.
func fresh(items []conversation.Item) int {
	called := map[string]bool{}
	for _, it := range items {
		if it.Kind == conversation.ItemToolCall {
			called[it.Response] = true
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind == conversation.ItemUserMessage || it.Kind == conversation.ItemAgentMessage && !called[it.Response] {
			return i + 1
		}
	}
	return 0
}

// A compaction's response is no step of the turn's work.
func responses(v conversation.View, turn string) int {
	compactions := map[string]bool{}
	for _, it := range v.ItemsOf(turn) {
		if it.Kind == conversation.ItemCompaction {
			compactions[it.Response] = true
		}
	}
	n := 0
	for _, r := range v.Responses {
		if r.Turn == turn && !compactions[r.ID] {
			n++
		}
	}
	return n
}

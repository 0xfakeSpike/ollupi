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
	DelegateSub Name = "delegate-sub-agent"
	HubMain     Name = "hub-main-agent"
)

// PlanFile is the Markdown file at the root of the hub workspace's folder where the hub keeps the user's plan
// with its file tools. The desktop app reads the same file to show the plan.
const PlanFile = "TODO.md"

var Names = []Name{FullMain, CompactMain, GeneralSub, BrowserSub, DelegateSub, HubMain}

const (
	ReadFileTool          = "read_file"
	RunCommandTool        = "run_command"
	PlanTool              = "update_plan"
	ReadOutputTool        = "read_tool_output"
	LoadToolsTool         = "load_tools"
	ListWorkspacesTool    = "list_workspaces"
	StartConversationTool = "start_conversation"
	SendMessageTool       = "send_message"
	ListConversationsTool = "list_conversations"
	ReadConversationTool  = "read_conversation"
	BrowserProvider       = "browser"
	OutputsProvider       = "context"
	SkillsProvider        = "skills"
	PlanProvider          = "plan"
	SubagentsProvider     = "subagents"
	CronProvider          = "cron"
	WorkspaceProvider     = "workspace"
	HubProvider           = "hub"
	compactBelowTokens    = 16_384
)

type Role struct {
	Name        Name
	Rules       string
	AnswersUser bool
	Own         []policy.Layer
	Core        []string
	Recalls     bool
	Limits
}

type Limits struct {
	MaxModelCalls          int
	MaxToolCalls           int
	MaxConsecutiveFailures int
}

var standard = Limits{MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10}

var roles = map[Name]Role{
	FullMain: {Name: FullMain, Rules: fullMainRules, AnswersUser: true, Recalls: true,
		Own: []policy.Layer{{Of: "the main agent's role", Without: []string{HubProvider}}}, Limits: standard},
	CompactMain: {Name: CompactMain, Rules: compactMainRules, AnswersUser: true, Recalls: true, Core: []string{ReadFileTool, RunCommandTool},
		Own: []policy.Layer{{Of: "the main agent's role", Without: []string{HubProvider}}}, Limits: standard},
	HubMain: {Name: HubMain, Rules: hubMainRules, AnswersUser: true, Recalls: true,
		Own: []policy.Layer{{Of: "the hub's role", Without: []string{SubagentsProvider, BrowserProvider},
			Settings: policy.Settings{Rules: policy.Rules{Allow: []string{StartConversationTool, SendMessageTool}}}}}, Limits: standard},
	GeneralSub: {Name: GeneralSub, Rules: generalSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the general subagent's role", Without: []string{SubagentsProvider, CronProvider, HubProvider}}}, Limits: standard},
	BrowserSub: {Name: BrowserSub, Rules: browserSubRules,
		Own: []policy.Layer{{Of: "the browser subagent's role", Providers: []string{BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: standard},
	DelegateSub: {Name: DelegateSub, Rules: delegateSubRules, AnswersUser: true, Recalls: true,
		Own: []policy.Layer{{Of: "the delegate subagent's role", Without: []string{SubagentsProvider, CronProvider, HubProvider}}}, Limits: standard},
}

func RoleOf(name Name) (Role, bool) {
	r, ok := roles[name]
	return r, ok
}

func MainRole(inputTokens int) Name {
	if inputTokens < compactBelowTokens {
		return CompactMain
	}
	return FullMain
}

type Action interface{ action() }

type Wait struct{}

type RunTools struct{ Calls []conversation.Item }

type SkipTools struct {
	Calls  []conversation.Item
	Reason string
}

type CallModel struct {
	NoTools bool
	Notice  *conversation.Notice
}

type Finish struct{}

func (Wait) action()      {}
func (RunTools) action()  {}
func (SkipTools) action() {}
func (CallModel) action() {}
func (Finish) action()    {}

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
	from := fresh(items)
	stopped := stop(v, turn.ID, items, from, r)
	switch {
	case len(runnable) > 0 && stopped != "":
		return SkipTools{Calls: runnable, Reason: stopped}
	case len(runnable) > 0:
		return RunTools{Calls: runnable}
	case v.Answered(turn.ID):
		return Finish{}
	}
	told := slices.ContainsFunc(items[from:], func(it conversation.Item) bool { return it.Kind == conversation.ItemNotice })
	if told {
		return CallModel{NoTools: true}
	}
	if stopped != "" {
		return CallModel{NoTools: true, Notice: &conversation.Notice{Text: fmt.Sprintf(concludeNotice, stopped)}}
	}
	return CallModel{}
}

func stop(v conversation.View, turn string, items []conversation.Item, from int, r Role) string {
	if i := slices.IndexFunc(items[from:], declined); i >= 0 {
		return fmt.Sprintf("the user declined %s", items[from+i].Call.Tool)
	}
	calls := 0
	for _, it := range items {
		if it.Kind == conversation.ItemToolCall && it.Status != conversation.Pending {
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

func responses(v conversation.View, turn string) int {
	n := 0
	for _, r := range v.Responses {
		if r.Turn == turn && !r.Compaction {
			n++
		}
	}
	return n
}

func Launchable(visible []policy.Tool) []policy.Tool {
	out := slices.Clone(visible)
	for _, name := range []Name{GeneralSub, BrowserSub, DelegateSub} {
		own := roles[name].Own
		workable := slices.ContainsFunc(visible, func(t policy.Tool) bool {
			return t.Provider != OutputsProvider && policy.Kept(own, t)
		})
		if !workable {
			out = slices.DeleteFunc(out, func(t policy.Tool) bool { return t.Name == string(name) })
		}
	}
	return out
}

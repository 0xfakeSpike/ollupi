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
	DesignSub   Name = "design-sub-agent"
	CodebaseSub Name = "codebase-sub-agent"
	FrontendSub Name = "frontend-sub-agent"
	BackendSub  Name = "backend-sub-agent"
	WritingSub  Name = "writing-sub-agent"
	ResearchSub Name = "research-sub-agent"
	VerifySub   Name = "verify-sub-agent"
	HubMain     Name = "hub-main-agent"
)

var Names = []Name{FullMain, CompactMain, GeneralSub, BrowserSub, DesignSub, CodebaseSub, FrontendSub, BackendSub, WritingSub, ResearchSub, VerifySub, HubMain}

// Kind says where a role's reply goes: back to the agent that launched it, or to the user.
type Kind string

const (
	// Main answers the user in the conversation it runs.
	MainKind Kind = "main"
	// Reporting answers the agent that launched it, which is the only one that reads the reply.
	ReportingKind Kind = "reporting"
)

const (
	ReadFileTool          = "read_file"
	RunCommandTool        = "run_command"
	EditFileTool          = "edit_file"
	WriteFileTool         = "write_file"
	ClaudeTool            = "claude"
	PlanTool              = "update_plan"
	ReadOutputTool        = "read_tool_output"
	LoadToolsTool         = "load_tools"
	ListWorkspacesTool    = "list_workspaces"
	StartConversationTool = "start_conversation"
	SendMessageTool       = "send_message"
	ListConversationsTool = "list_conversations"
	ReadConversationTool  = "read_conversation"
	AddTodoTool           = "add_todo"
	UpdateTodoTool        = "update_todo"
	ListTodosTool         = "list_todos"
	BrowserProvider       = "browser"
	WebProvider           = "web"
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
	Name    Name
	Kind    Kind
	Rules   string
	Own     []policy.Layer
	Core    []string
	Recalls bool
	Limits
}

// AnswersUser says whether the role's reply is shown to the user, rather than to the agent that launched it.
func (r Role) AnswersUser() bool { return r.Kind != ReportingKind }

// Reporting says whether a role's reply goes back to the agent that launched it.
func Reporting(name string) bool { return roles[Name(name)].Kind == ReportingKind }

type Limits struct {
	MaxModelCalls          int
	MaxToolCalls           int
	MaxConsecutiveFailures int
}

var standard = Limits{MaxModelCalls: 200, MaxToolCalls: 400, MaxConsecutiveFailures: 10}

var readOnly = policy.Settings{Rules: policy.Rules{Deny: []string{EditFileTool, WriteFileTool, ClaudeTool}}}

// deep suits a subagent that reads a whole project: more reading than writing, many small reads.
var deep = Limits{MaxModelCalls: 400, MaxToolCalls: 1_200, MaxConsecutiveFailures: 10}

var roles = map[Name]Role{
	FullMain: {Name: FullMain, Kind: MainKind, Rules: fullMainRules, Recalls: true,
		Own: []policy.Layer{{Of: "the main agent's role", Without: []string{HubProvider}}}, Limits: standard},
	CompactMain: {Name: CompactMain, Kind: MainKind, Rules: compactMainRules, Recalls: true, Core: []string{ReadFileTool, RunCommandTool},
		Own: []policy.Layer{{Of: "the main agent's role", Without: []string{HubProvider}}}, Limits: standard},
	HubMain: {Name: HubMain, Kind: MainKind, Rules: hubMainRules, Recalls: true,
		Own: []policy.Layer{{Of: "the hub's role", Without: []string{SubagentsProvider, BrowserProvider},
			Settings: policy.Settings{Rules: policy.Rules{Allow: []string{StartConversationTool, SendMessageTool}}}}}, Limits: standard},
	GeneralSub: {Name: GeneralSub, Kind: ReportingKind, Rules: generalSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the general subagent's role", Without: []string{SubagentsProvider, CronProvider, HubProvider}}}, Limits: standard},
	BrowserSub: {Name: BrowserSub, Kind: ReportingKind, Rules: browserSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the browser subagent's role", Providers: []string{WorkspaceProvider, BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: standard},
	DesignSub: {Name: DesignSub, Kind: ReportingKind, Rules: designSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the design subagent's role", Providers: []string{WorkspaceProvider, BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: standard},
	CodebaseSub: {Name: CodebaseSub, Kind: ReportingKind, Rules: codebaseSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the codebase subagent's role", Providers: []string{WorkspaceProvider, OutputsProvider, SkillsProvider, PlanProvider},
			Settings: readOnly}}, Limits: deep},
	FrontendSub: {Name: FrontendSub, Kind: ReportingKind, Rules: frontendSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the frontend subagent's role", Providers: []string{WorkspaceProvider, BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: standard},
	BackendSub: {Name: BackendSub, Kind: ReportingKind, Rules: backendSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the backend subagent's role", Providers: []string{WorkspaceProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: deep},
	WritingSub: {Name: WritingSub, Kind: ReportingKind, Rules: writingSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the writing subagent's role", Providers: []string{WorkspaceProvider, OutputsProvider, SkillsProvider, PlanProvider}}}, Limits: standard},
	ResearchSub: {Name: ResearchSub, Kind: ReportingKind, Rules: researchSubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the research subagent's role", Providers: []string{WorkspaceProvider, WebProvider, BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider},
			Settings: readOnly}}, Limits: deep},
	VerifySub: {Name: VerifySub, Kind: ReportingKind, Rules: verifySubRules, Recalls: true,
		Own: []policy.Layer{{Of: "the verify subagent's role", Providers: []string{WorkspaceProvider, BrowserProvider, OutputsProvider, SkillsProvider, PlanProvider},
			Settings: readOnly}}, Limits: standard},
}

func RoleOf(name Name) (Role, bool) {
	r, ok := roles[name]
	return r, ok
}

// Having names the roles whose own layers keep a tool, in the order of Names: the agents that can be
// offered it, before a workspace or a conversation's settings take any away.
func Having(t policy.Tool) []Name {
	out := []Name{}
	for _, name := range Names {
		if policy.Kept(roles[name].Own, t) {
			out = append(out, name)
		}
	}
	return out
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

// Launchable drops the subagent tools whose role could not work here at all: a subagent is offered
// only when the tools it would keep are still visible, since the rest it could only fail with.
// Which roles there are comes from the table, not from a list repeated here.
func Launchable(visible []policy.Tool) []policy.Tool {
	out := slices.Clone(visible)
	for _, role := range roles {
		if role.Kind == MainKind {
			continue
		}
		workable := slices.ContainsFunc(visible, func(t policy.Tool) bool {
			return t.Provider != OutputsProvider && policy.Kept(role.Own, t)
		})
		if !workable {
			out = slices.DeleteFunc(out, func(t policy.Tool) bool { return t.Name == string(role.Name) })
		}
	}
	return out
}

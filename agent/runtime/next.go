package runtime

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// State is a run at one point: the turn and its items, with what the run decides by. Next decides from it alone what
// the run does next; the items say everything that happened, so a run rebuilt from them after a crash goes on alike.
type State struct {
	Turn repo.TurnRow
	Role role.Role
	// Prompt is the agent's own prompt, and Root the project the conversation works in.
	Prompt, Root string
	Skills       []skills.Skill
	Window       *contextmanager.Manager
	Permissions  *permission.Checker
	History      []repo.ItemRow
	Items        []repo.ItemRow
	// Inspections are what preparing the pending calls and reading what they would do found, by item, since the
	// latest model response.
	Inspections map[string]Inspection
	// Prompted is the Seq of the newest item the run's latest model request was built from; 0 before its first.
	Prompted int
	// MemoryOff says the run has no memory to load, though its role would load it.
	MemoryOff bool
}

// An Inspection is what was found out about a pending call before deciding on it.
type Inspection struct {
	Content    tools.Content
	HasContent bool
	// Err is why the call could not be prepared or read; it is refused with it.
	Err error
}

// A Step is what Next says the run does next.
type Step interface{ isStep() }

// Wait ends the run until what an item waits on arrives.
type Wait struct{}

// Done ends the run with its final answer.
type Done struct{}

// LoadMemory records a snapshot of the user's memory within Budget tokens.
type LoadMemory struct{ Budget int }

// Inspect prepares Call when Prepare says so and reads what it would do, for Next to decide on it.
type Inspect struct {
	Call    repo.ItemRow
	Args    map[string]any
	Prepare bool
}

// Calls records what was decided on pending calls, then runs the calls of Run at the same time.
type Calls struct {
	Records  []repo.ItemRow
	Run      []job
	Deferred []tools.ToolDescriptor
}

// Stop records Notice, which stops tool use: the model answers next without tools.
type Stop struct{ Notice repo.ItemRow }

// Compact summarizes the conversation with Prompt.
type Compact struct {
	Prompt contextmanager.Prompt
	Offer  offer
}

// CallModel sends Prompt, which was built from the items up to Seq Prompted. Stopped is why tool use stopped, when
// it did.
type CallModel struct {
	Prompt   contextmanager.Prompt
	Choice   llm.ToolChoice
	Offer    offer
	Prompted int
	Stopped  string
}

func (Wait) isStep()       {}
func (Done) isStep()       {}
func (LoadMemory) isStep() {}
func (Inspect) isStep()    {}
func (Calls) isStep()      {}
func (Stop) isStep()       {}
func (Compact) isStep()    {}
func (CallModel) isStep()  {}

// Next is what the run does next: load the memory the role needs, settle the pending calls, stop tool use, or ask the
// model; or end, waiting or with the final answer.
func Next(s State) (Step, error) {
	if s.Role.Memory && !s.MemoryOff && !hasSnapshot(slices.Concat(s.History, s.Items)) {
		return LoadMemory{Budget: s.Window.MemoryBudget()}, nil
	}
	of := s.offer()
	plan, inspect, err := s.planCalls(of)
	if err != nil {
		return nil, err
	}
	if inspect != nil {
		return *inspect, nil
	}
	if len(plan.records) > 0 || len(plan.run) > 0 {
		return Calls{Records: plan.records, Run: plan.firstRun(), Deferred: of.deferred}, nil
	}
	if waiting(s.Items) {
		return Wait{}, nil
	}
	if plan.halt != "" {
		return s.stop(plan.halt), nil
	}
	return s.ask(of)
}

// ask is the model request that comes next, or the end of the run when the latest response is the final answer and
// nothing came since it was asked for.
func (s State) ask(of offer) (Step, error) {
	response, stop := lastIndex(s.Items, isResponse), lastIndex(s.Items, isStopNotice)
	choice, stopped := llm.ToolChoiceAuto, ""
	switch {
	case stop > response:
		choice, stopped = llm.ToolChoiceNone, strings.TrimSuffix(strings.TrimPrefix(firstLine(s.Items[stop].Text), stopPrefix), ".")
	case response >= 0 && s.Prompted > 0 && !hasCalls(s.Items, s.Items[response].ResponseID) && !inputAfter(s.Items, s.Prompted):
		return Done{}, nil
	case modelCalls(s.Items) >= maxModelCalls:
		return s.stop(fmt.Sprintf("reached the model call limit (%d)", maxModelCalls)), nil
	}
	in := s.contextInput(of)
	prompt, err := s.Window.Build(in)
	if err != nil {
		return nil, fmt.Errorf("runtime: %w", err)
	}
	if prompt.NeedsCompaction && !slices.ContainsFunc(s.Items[response+1:], isCompaction) {
		summary, err := s.Window.CompactionPrompt(in)
		if err != nil {
			return nil, fmt.Errorf("runtime: %w", err)
		}
		return Compact{Prompt: summary, Offer: of}, nil
	}
	prompted := 0
	for _, it := range s.Items {
		prompted = max(prompted, it.Seq)
	}
	return CallModel{Prompt: prompt, Choice: choice, Offer: of, Prompted: prompted, Stopped: stopped}, nil
}

const (
	stopPrefix        = "[runtime] Tool use has stopped: "
	finalAnswerNotice = stopPrefix + `%s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`
)

func (s State) stop(reason string) Stop {
	now := time.Now().UTC()
	return Stop{Notice: repo.ItemRow{
		ID: uuid.NewString(), TurnID: s.Turn.ID, Type: repo.ItemError, Status: repo.ItemCompleted,
		Text: fmt.Sprintf(finalAnswerNotice, reason), StartedAt: now, CompletedAt: &now,
	}}
}

// An offer is the role's split of the tools the run may use, as the run's items and permissions are at one point: the
// ones sent to the model, and the deferred ones it has to load first.
type offer struct {
	offered, deferred []tools.ToolDescriptor
}

func (s State) offer() offer {
	offered, deferred := s.Role.Offer(s.Permissions.Tools(), s.Window, slices.Concat(s.History, s.Items))
	return offer{offered: offered, deferred: deferred}
}

func (o offer) tool(name string) (tools.ToolDescriptor, bool) {
	i := slices.IndexFunc(o.offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return o.offered[i], true
}

func (s State) contextInput(of offer) contextmanager.Input {
	return contextmanager.Input{
		Items: slices.Concat(s.History, s.Items),
		System: s.Role.System(role.Input{
			Agent: s.Prompt, Root: s.Root, Skills: s.Skills, SkillTokens: s.Window.SkillBudget(), Deferred: of.deferred,
		}),
		Tools: role.Specs(of.offered),
	}
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

func waiting(items []repo.ItemRow) bool {
	return slices.ContainsFunc(items, func(it repo.ItemRow) bool { return it.WaitsOnUser() || it.Awaits() })
}

func isResponse(it repo.ItemRow) bool {
	return it.ResponseID != "" && it.Type != repo.ItemContextCompaction
}

func isCompaction(it repo.ItemRow) bool {
	return it.Type == repo.ItemContextCompaction && it.Status == repo.ItemCompleted
}

func isStopNotice(it repo.ItemRow) bool {
	return it.Type == repo.ItemError && it.Status == repo.ItemCompleted && it.ResponseID == "" && strings.HasPrefix(it.Text, stopPrefix)
}

func lastIndex(items []repo.ItemRow, f func(repo.ItemRow) bool) int {
	for i := len(items) - 1; i >= 0; i-- {
		if f(items[i]) {
			return i
		}
	}
	return -1
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func hasCalls(items []repo.ItemRow, responseID string) bool {
	return slices.ContainsFunc(items, func(it repo.ItemRow) bool { return it.IsCall() && it.ResponseID == responseID })
}

// inputAfter says a message or a background result came after the item of Seq seq.
func inputAfter(items []repo.ItemRow, seq int) bool {
	return slices.ContainsFunc(items, func(it repo.ItemRow) bool {
		return (it.Type == repo.ItemUserMessage || it.Type == repo.ItemBackgroundResult) && it.Seq > seq
	})
}

func modelCalls(items []repo.ItemRow) int {
	seen := map[string]bool{}
	for _, it := range items {
		if isResponse(it) {
			seen[it.ResponseID] = true
		}
	}
	return len(seen)
}

func completed(it repo.ItemRow, status repo.ItemStatus) repo.ItemRow {
	now := time.Now().UTC()
	it.Status, it.CompletedAt = status, &now
	return it
}

func withError(it repo.ItemRow, text string) repo.ItemRow {
	it.Error = text
	return it
}

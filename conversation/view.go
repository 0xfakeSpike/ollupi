package conversation

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// View is a conversation folded from its entries. A store keeps it beside them, so that it is read rather than folded
// again, and folds each entry into it as it appends the entry. Apply indexes a view decoded from a store the first
// time it folds an entry into it.
type View struct {
	Head      int64
	Turns     []Turn
	Items     []Item
	Responses []Response

	turn map[string]int
	item map[string]int
}

type Turn struct {
	ID       string
	Model    llm.Model
	Role     string
	CronJob  string
	QueuedAt time.Time
	Ended    *Ending
	Usage    Usage
}

type Ending struct {
	Status End
	Reason string
	At     time.Time
}

type Response struct {
	ID     string
	Turn   string
	Seq    int64
	Usage  Usage
	Stats  ContextStats
	Native []byte
}

type ItemKind string

const (
	ItemUserMessage  ItemKind = "userMessage"
	ItemAgentMessage ItemKind = "agentMessage"
	ItemReasoning    ItemKind = "reasoning"
	ItemToolCall     ItemKind = "toolCall"
	ItemReport       ItemKind = "agentReport"
	ItemCompaction   ItemKind = "contextCompaction"
	ItemMemory       ItemKind = "memoryContext"
	ItemError        ItemKind = "error"
	ItemNotice       ItemKind = "notice"
)

type ItemStatus string

const (
	Pending    ItemStatus = "pending"
	InProgress ItemStatus = "inProgress"
	Done       ItemStatus = "completed"
	Errored    ItemStatus = "failed"
	Declined   ItemStatus = "declined"
)

// Seq is the item's first entry and Updated its latest.
type Item struct {
	ID          string
	Turn        string
	Kind        ItemKind
	Status      ItemStatus
	Seq         int64
	Updated     int64
	StartedAt   time.Time
	CompletedAt *time.Time
	Text        string
	Attachments []Attachment
	Response    string
	Call        *ToolCall
	Approval    *Approval
	Input       *Input
	Result      *Result
	Error       string
	Child       string
	Reported    bool
	Launch      string
	Through     int64
	Stats       *ContextStats
	Memories    []MemoryRef
}

type Approval struct {
	ApprovalAsked
	AskedAt   time.Time
	Decision  Decision
	By        string
	DecidedAt *time.Time
}

func (a Approval) Decisions() []Decision {
	switch {
	case a.Rule != "":
		return []Decision{Accept, Decline}
	case len(a.Suggested) == 0:
		return []Decision{Accept, AcceptForSession, Decline}
	}
	return []Decision{Accept, AcceptForSession, AcceptAlways, Decline}
}

type Input struct {
	Request  json.RawMessage
	AskedAt  time.Time
	Answered bool
}

type Result struct {
	Output    json.RawMessage
	Artifacts []Artifact
}

func Fold(entries []Entry) View {
	var v View
	for _, e := range entries {
		v.Apply(e)
	}
	return v
}

// Apply folds e into the view; Check says first whether e may be appended.
func (v *View) Apply(e Entry) {
	if v.turn == nil {
		v.index()
	}
	v.Head = e.Seq
	switch b := e.Body.(type) {
	case TurnQueued:
		v.turn[e.Turn] = len(v.Turns)
		v.Turns = append(v.Turns, Turn{ID: e.Turn, Model: b.Model, Role: b.Role, CronJob: b.CronJob, QueuedAt: e.At})
	case TurnEnded:
		v.Turns[v.turn[e.Turn]].Ended = &Ending{Status: b.Status, Reason: b.Reason, At: e.At}
	case UserMessage:
		v.add(e, Item{Kind: ItemUserMessage, Status: Done, Text: b.Text, Attachments: b.Attachments})
	case AgentMessage:
		v.add(e, Item{Kind: ItemAgentMessage, Status: Done, Text: b.Text, Response: b.Response})
	case Reasoning:
		v.add(e, Item{Kind: ItemReasoning, Status: Done, Text: b.Text, Response: b.Response})
	case ModelResponded:
		v.Responses = append(v.Responses, Response{ID: b.Response, Turn: e.Turn, Seq: e.Seq, Usage: b.Usage, Stats: b.Stats, Native: b.Native})
		t := &v.Turns[v.turn[e.Turn]]
		t.Usage = t.Usage.Plus(b.Usage)
	case ModelFailed:
		v.add(e, Item{Kind: ItemError, Status: Errored, Error: b.Error})
	case ToolCall:
		call := b
		v.add(e, Item{Kind: ItemToolCall, Status: Pending, Call: &call, Response: b.Response})
	case ApprovalAsked:
		v.update(e, func(it *Item) { it.Approval = &Approval{ApprovalAsked: b, AskedAt: e.At} })
	case Decided:
		v.update(e, func(it *Item) {
			at := e.At
			it.Approval.Decision, it.Approval.By, it.Approval.DecidedAt = b.Decision, b.By, &at
			if b.Decision == Decline {
				it.finish(Declined, e.At)
				it.Error = "the user declined this call"
			}
		})
	case ToolStarted:
		v.update(e, func(it *Item) { it.Status = InProgress })
	case InputAsked:
		v.update(e, func(it *Item) { it.Input = &Input{Request: b.Request, AskedAt: e.At} })
	case Answered:
		v.update(e, func(it *Item) {
			it.Input.Answered = true
			if b.Skipped {
				it.finish(Declined, e.At)
				it.Error = "the user skipped these questions; go on without their answers"
				return
			}
			it.finish(Done, e.At)
			it.Result = &Result{Output: b.Output}
		})
	case ToolResult:
		v.update(e, func(it *Item) {
			switch {
			case b.Declined:
				it.finish(Declined, e.At)
				it.Error = b.Error
			case b.Error != "":
				it.finish(Errored, e.At)
				it.Error = b.Error
			default:
				it.finish(Done, e.At)
				it.Result = &Result{Output: b.Output, Artifacts: b.Artifacts}
			}
		})
	case ChildLaunched:
		v.update(e, func(it *Item) { it.Child = b.Child })
	case ChildReported:
		report := Item{Kind: ItemReport, Status: Done, Text: b.Task, Child: b.Child, Launch: b.Launch, Result: &Result{Output: b.Output}}
		if b.Error != "" {
			report.Status, report.Error, report.Result = Errored, b.Error, nil
		}
		v.add(e, report)
		v.Items[v.item[b.Launch]].Reported = true
	case Compacted:
		stats := b.Stats
		v.add(e, Item{Kind: ItemCompaction, Status: Done, Text: b.Summary, Through: b.Through, Response: b.Response, Stats: &stats})
	case Recalled:
		v.add(e, Item{Kind: ItemMemory, Status: Done, Text: b.Text, Memories: b.Memories})
	case Notice:
		v.add(e, Item{Kind: ItemNotice, Status: Done, Text: b.Text})
	}
}

func (v *View) index() {
	v.turn, v.item = map[string]int{}, map[string]int{}
	for i, t := range v.Turns {
		v.turn[t.ID] = i
	}
	for i, it := range v.Items {
		v.item[it.ID] = i
	}
}

func (v *View) add(e Entry, it Item) {
	it.ID, it.Turn, it.Seq, it.Updated, it.StartedAt = e.Item, e.Turn, e.Seq, e.Seq, e.At
	if it.Status != Pending {
		at := e.At
		it.CompletedAt = &at
	}
	v.item[it.ID] = len(v.Items)
	v.Items = append(v.Items, it)
}

func (v *View) update(e Entry, change func(*Item)) {
	it := &v.Items[v.item[e.Item]]
	change(it)
	it.Updated = e.Seq
}

func (it *Item) finish(status ItemStatus, at time.Time) {
	it.Status, it.CompletedAt = status, &at
}

func (v View) TurnOf(id string) (Turn, bool) {
	i := slices.IndexFunc(v.Turns, func(t Turn) bool { return t.ID == id })
	if i < 0 {
		return Turn{}, false
	}
	return v.Turns[i], true
}

func (v View) ItemOf(id string) (Item, bool) {
	i := slices.IndexFunc(v.Items, func(it Item) bool { return it.ID == id })
	if i < 0 {
		return Item{}, false
	}
	return v.Items[i], true
}

// Active is the turn in progress: the first that has not ended. Those after it wait behind it.
func (v View) Active() (Turn, bool) {
	i := slices.IndexFunc(v.Turns, func(t Turn) bool { return t.Ended == nil })
	if i < 0 {
		return Turn{}, false
	}
	return v.Turns[i], true
}

func (v View) Behind() []Turn {
	var out []Turn
	active, ok := v.Active()
	for _, t := range v.Turns {
		if ok && t.Ended == nil && t.ID != active.ID {
			out = append(out, t)
		}
	}
	return out
}

func (v View) ItemsOf(turn string) []Item {
	var out []Item
	for _, it := range v.Items {
		if it.Turn == turn {
			out = append(out, it)
		}
	}
	return out
}

type Wait string

const (
	WaitNone     Wait = ""
	WaitApproval Wait = "approval"
	WaitInput    Wait = "input"
	WaitChildren Wait = "children"
)

// Waiting says what the turn in progress waits on, the user first: a turn waits on its subagents only once it has
// answered, and the reports bring it back.
func (v View) Waiting() Wait {
	active, ok := v.Active()
	if !ok {
		return WaitNone
	}
	items := v.ItemsOf(active.ID)
	switch {
	case slices.ContainsFunc(items, Item.waitsForApproval):
		return WaitApproval
	case slices.ContainsFunc(items, Item.waitsForInput):
		return WaitInput
	case v.Answered(active.ID) && slices.ContainsFunc(items, Item.awaitsReport):
		return WaitChildren
	}
	return WaitNone
}

// Requests are what the turn in progress asks the user, in the order it asked.
func (v View) Requests() []Item {
	active, ok := v.Active()
	if !ok {
		return nil
	}
	var out []Item
	for _, it := range v.ItemsOf(active.ID) {
		if it.waitsForApproval() || it.waitsForInput() {
			out = append(out, it)
		}
	}
	return out
}

// Runnable says whether the turn in progress has work a runner can do now.
func (v View) Runnable() bool {
	_, ok := v.Active()
	return ok && v.Waiting() == WaitNone
}

// Answered says whether the turn's latest model response answered without calling a tool, and nothing has come in
// since that the model has not seen: a message the user sent, or a subagent's report.
func (v View) Answered(turn string) bool {
	i := len(v.Responses) - 1
	for i >= 0 && v.Responses[i].Turn != turn {
		i--
	}
	if i < 0 {
		return false
	}
	latest := v.Responses[i]
	answered := false
	for _, it := range v.ItemsOf(turn) {
		switch {
		case it.Response == latest.ID && it.Kind == ItemToolCall:
			return false
		case it.Response == latest.ID && it.Kind == ItemAgentMessage:
			answered = true
		case (it.Kind == ItemUserMessage || it.Kind == ItemReport) && it.Seq > latest.Seq:
			return false
		}
	}
	return answered
}

type ThreadStatus string

const (
	ThreadIdle               ThreadStatus = "idle"
	ThreadActive             ThreadStatus = "active"
	ThreadWaitingOnApproval  ThreadStatus = "waitingOnApproval"
	ThreadWaitingOnUserInput ThreadStatus = "waitingOnUserInput"
)

// Status is this conversation's alone; a store rolls up its running subagents' into it.
func (v View) Status() ThreadStatus {
	if _, ok := v.Active(); !ok {
		return ThreadIdle
	}
	switch v.Waiting() {
	case WaitApproval:
		return ThreadWaitingOnApproval
	case WaitInput:
		return ThreadWaitingOnUserInput
	}
	return ThreadActive
}

func (it Item) open() bool {
	return it.Kind == ItemToolCall && (it.Status == Pending || it.Status == InProgress)
}

func (it Item) waitsForApproval() bool {
	return it.Kind == ItemToolCall && it.Status == Pending && it.Approval != nil && it.Approval.Decision == ""
}

func (it Item) waitsForInput() bool {
	return it.Kind == ItemToolCall && it.Status == InProgress && it.Input != nil && !it.Input.Answered
}

func (it Item) awaitsReport() bool {
	return it.Child != "" && !it.Reported
}

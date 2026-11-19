package conversation

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type log struct {
	t *testing.T
	v View
}

func (l *log) append(turn, item string, b Body) {
	l.t.Helper()
	e := Entry{Seq: l.v.Head + 1, Turn: turn, Item: item, At: time.Unix(l.v.Head+1, 0), Body: b}
	if err := Check(l.v, e); err != nil {
		l.t.Fatalf("appending %s: %v", KindOf(b), err)
	}
	l.v.Apply(e)
}

func (l *log) refused(turn, item string, b Body) {
	l.t.Helper()
	e := Entry{Seq: l.v.Head + 1, Turn: turn, Item: item, Body: b}
	if err := Check(l.v, e); !errors.Is(err, ErrRefused) {
		l.t.Errorf("appending %s %s was not refused: %v", KindOf(b), item, err)
	}
}

func (l *log) item(id string) Item {
	l.t.Helper()
	it, ok := l.v.ItemOf(id)
	if !ok {
		l.t.Fatalf("no item %s", id)
	}
	return it
}

func TestATurnCallsAToolOnceApprovedAndEndsWhenItAnswers(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{Role: "full-main-agent"})
	l.append("t1", "m1", UserMessage{Text: "list the files"})
	if !l.v.Runnable() || l.v.Status() != ThreadActive {
		t.Fatalf("a turn with a message: runnable %v, status %s", l.v.Runnable(), l.v.Status())
	}
	l.append("t1", "", ModelResponded{Response: "r1", Usage: Usage{InputTokens: 10, OutputTokens: 2}})
	l.append("t1", "c1", ToolCall{Call: "call_1", Tool: "run_command", Arguments: `{"command":"ls"}`, Response: "r1"})
	l.append("t1", "c1", ApprovalAsked{Approval: "a1", Reason: "runs a command", Suggested: []string{"run_command(ls)"}})
	if l.v.Waiting() != WaitApproval || l.v.Runnable() || l.v.Status() != ThreadWaitingOnApproval || len(l.v.Requests()) != 1 {
		t.Fatalf("waiting on an approval: %s, runnable %v, status %s, %d requests", l.v.Waiting(), l.v.Runnable(), l.v.Status(), len(l.v.Requests()))
	}
	l.refused("t1", "c1", ToolStarted{})
	l.refused("t1", "", TurnEnded{Status: Completed})
	l.append("t1", "c1", Decided{Decision: AcceptAlways, By: "gateway"})
	l.refused("t1", "c1", Decided{Decision: Accept})
	if !l.v.Runnable() {
		t.Fatal("an approved call does not make the turn runnable")
	}
	l.append("t1", "c1", ToolStarted{})
	l.append("t1", "c1", ToolResult{Output: json.RawMessage(`"a.go"`)})
	if c := l.item("c1"); c.Status != Done || string(c.Result.Output) != `"a.go"` || c.Approval.Decision != AcceptAlways {
		t.Errorf("the call: %+v", c)
	}
	if l.v.Answered("t1") {
		t.Error("a turn whose latest response called a tool has answered")
	}
	l.append("t1", "", ModelResponded{Response: "r2", Usage: Usage{InputTokens: 20, OutputTokens: 3}})
	l.append("t1", "a1", AgentMessage{Text: "a.go", Response: "r2"})
	if !l.v.Answered("t1") || !l.v.Runnable() {
		t.Fatalf("an answer: answered %v, runnable %v", l.v.Answered("t1"), l.v.Runnable())
	}
	l.append("t1", "", TurnEnded{Status: Completed})
	if _, ok := l.v.Active(); ok || l.v.Status() != ThreadIdle {
		t.Errorf("after the end: status %s", l.v.Status())
	}
	if turn, _ := l.v.TurnOf("t1"); turn.Usage.InputTokens != 30 || turn.Usage.OutputTokens != 5 {
		t.Errorf("usage %+v", turn.Usage)
	}
	l.refused("t1", "m2", UserMessage{Text: "late"})
}

func TestAMessageSentMidTurnUndoesTheAnswer(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{})
	l.append("t1", "m1", UserMessage{Text: "hi"})
	l.append("t1", "", ModelResponded{Response: "r1"})
	l.append("t1", "a1", AgentMessage{Text: "hello", Response: "r1"})
	l.append("t1", "m2", UserMessage{Text: "and one more thing"})
	if l.v.Answered("t1") {
		t.Error("a message after the answer leaves the turn answered")
	}
}

func TestATurnWaitsOnItsSubagentsOnlyOnceItHasAnswered(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{})
	l.append("t1", "m1", UserMessage{Text: "look into it"})
	l.append("t1", "", ModelResponded{Response: "r1"})
	l.append("t1", "c1", ToolCall{Call: "call_1", Tool: "general", Response: "r1"})
	l.append("t1", "c1", ToolStarted{})
	l.refused("t1", "c1", ChildLaunched{})
	l.append("t1", "c1", ChildLaunched{Child: "child"})
	l.append("t1", "c1", ToolResult{Output: json.RawMessage(`{"subagent":"child"}`)})
	if l.v.Waiting() != WaitNone || !l.v.Runnable() {
		t.Fatalf("before the answer: %s", l.v.Waiting())
	}
	l.append("t1", "", ModelResponded{Response: "r2"})
	l.append("t1", "a1", AgentMessage{Text: "a subagent is on it", Response: "r2"})
	if l.v.Waiting() != WaitReports || l.v.Runnable() || l.v.Status() != ThreadActive {
		t.Fatalf("after the answer: %s, runnable %v, status %s", l.v.Waiting(), l.v.Runnable(), l.v.Status())
	}
	l.refused("t1", "rep", ChildReported{Launch: "c1", Child: "someone else"})
	l.append("t1", "rep", ChildReported{Launch: "c1", Child: "child", Task: "look into it", Output: json.RawMessage(`"found it"`)})
	l.refused("t1", "rep2", ChildReported{Launch: "c1", Child: "child"})
	if l.v.Waiting() != WaitNone || !l.v.Runnable() || l.v.Answered("t1") {
		t.Errorf("after the report: %s, runnable %v, answered %v", l.v.Waiting(), l.v.Runnable(), l.v.Answered("t1"))
	}
	if rep := l.item("rep"); rep.Kind != ItemReport || rep.Launch != "c1" || string(rep.Result.Output) != `"found it"` {
		t.Errorf("the report: %+v", rep)
	}
}

func TestABackgroundCallReportsItsResultLater(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{})
	l.append("t1", "m1", UserMessage{Text: "build it"})
	l.append("t1", "", ModelResponded{Response: "r1"})
	l.append("t1", "c1", ToolCall{Call: "call_1", Tool: "run_command", Response: "r1"})
	l.refused("t1", "c1", TaskLaunched{Task: "task"})
	l.append("t1", "c1", ToolStarted{})
	l.refused("t1", "c1", TaskLaunched{})
	l.append("t1", "c1", TaskLaunched{Task: "task"})
	l.refused("t1", "c1", ChildLaunched{Child: "child"})
	l.append("t1", "c1", ToolResult{Output: json.RawMessage(`{"task":"task"}`)})
	if c := l.item("c1"); c.Status != Done || !c.AwaitsReport() || l.v.Waiting() != WaitNone {
		t.Fatalf("the launched call %+v, waiting %s", c, l.v.Waiting())
	}
	l.append("t1", "", ModelResponded{Response: "r2"})
	l.append("t1", "a1", AgentMessage{Text: "building", Response: "r2"})
	if l.v.Waiting() != WaitReports || l.v.Runnable() {
		t.Fatalf("after the answer: %s", l.v.Waiting())
	}
	l.refused("t1", "rep", TaskReported{Launch: "c1", Task: "another"})
	l.append("t1", "rep", TaskReported{Launch: "c1", Task: "task", Output: json.RawMessage(`"ok"`)})
	l.refused("t1", "rep2", TaskReported{Launch: "c1", Task: "task"})
	if !l.v.Runnable() || l.v.Answered("t1") {
		t.Errorf("after the report: %s, answered %v", l.v.Waiting(), l.v.Answered("t1"))
	}
	if rep := l.item("rep"); rep.Kind != ItemReport || rep.Task != "task" || rep.Launch != "c1" || string(rep.Result.Output) != `"ok"` {
		t.Errorf("the report: %+v", rep)
	}
}

func TestACallWaitsOnTheUsersAnswer(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{})
	l.append("t1", "", ModelResponded{Response: "r1"})
	l.append("t1", "c1", ToolCall{Call: "call_1", Tool: "request_user_input", Response: "r1"})
	l.refused("t1", "c1", InputAsked{Request: json.RawMessage(`{}`)})
	l.append("t1", "c1", ToolStarted{})
	l.append("t1", "c1", InputAsked{Request: json.RawMessage(`{"questions":[]}`)})
	if l.v.Waiting() != WaitInput || l.v.Status() != ThreadWaitingOnUserInput {
		t.Fatalf("asking: %s, %s", l.v.Waiting(), l.v.Status())
	}
	l.refused("t1", "c1", ToolResult{Output: json.RawMessage(`"no"`)})
	l.append("t1", "c1", Answered{Skipped: true})
	if c := l.item("c1"); c.Status != Declined || c.Error == "" || l.v.Waiting() != WaitNone {
		t.Errorf("skipped: %+v, %s", c, l.v.Waiting())
	}
}

func TestATurnSentDuringAnotherWaitsBehindIt(t *testing.T) {
	l := &log{t: t}
	l.append("t1", "", TurnQueued{})
	l.append("t1", "m1", UserMessage{Text: "first"})
	l.append("t2", "", TurnQueued{})
	l.append("t2", "m2", UserMessage{Text: "second"})
	l.refused("t2", "", ModelResponded{Response: "r"})
	l.refused("t1", "", TurnQueued{})
	if active, _ := l.v.Active(); active.ID != "t1" || len(l.v.Queued()) != 1 {
		t.Fatalf("active %s, %d behind", active.ID, len(l.v.Queued()))
	}
	l.append("t1", "", TurnEnded{Status: Interrupted, Reason: "interrupted by the user"})
	if active, _ := l.v.Active(); active.ID != "t2" || len(l.v.Queued()) != 0 || !l.v.Runnable() {
		t.Errorf("after the first ended: active %s", active.ID)
	}
	l.refused("t2", "m2", UserMessage{Text: "again"})
	l.refused("t2", "c9", Decided{Decision: Accept})
}

func TestAViewFoldedAgainIsTheSame(t *testing.T) {
	l := &log{t: t}
	var entries []Entry
	add := func(turn, item string, b Body) {
		l.append(turn, item, b)
		entries = append(entries, Entry{Seq: l.v.Head, Turn: turn, Item: item, At: time.Unix(l.v.Head, 0), Body: b})
	}
	add("t1", "", TurnQueued{})
	add("t1", "m1", UserMessage{Text: "hi"})
	add("t1", "", ModelResponded{Response: "r1"})
	add("t1", "c1", ToolCall{Call: "x", Tool: "read_file", Response: "r1"})
	add("t1", "c1", ToolStarted{})
	add("t1", "c1", ToolResult{Error: "no such file"})
	folded := fold(entries)
	if folded.Head != l.v.Head || len(folded.Items) != len(l.v.Items) || folded.Items[1].Status != Errored {
		t.Errorf("folded again: head %d, %d items, %+v", folded.Head, len(folded.Items), folded.Items)
	}
}

func fold(entries []Entry) View {
	var v View
	for _, e := range entries {
		v.Apply(e)
	}
	return v
}

package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
)

type log struct {
	t *testing.T
	v conversation.View
}

func (l *log) add(turn, item string, b conversation.Body) {
	l.t.Helper()
	e := conversation.Entry{Seq: l.v.Head + 1, Turn: turn, Item: item, At: time.Unix(l.v.Head+1, 0), Body: b}
	if err := conversation.Check(l.v, e); err != nil {
		l.t.Fatal(err)
	}
	l.v.Apply(e)
}

func started(t *testing.T) *log {
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{Role: string(FullMain)})
	l.add("t", "m", conversation.UserMessage{Text: "do it"})
	return l
}

func full() Role {
	r, _ := RoleOf(FullMain)
	return r
}

func TestATurnCallsTheModelThenItsToolsThenFinishes(t *testing.T) {
	l := started(t)
	if a, ok := Next(l.v, full()).(CallModel); !ok || a.NoTools || a.Notice != nil {
		t.Fatalf("a new turn: %#v", Next(l.v, full()))
	}
	l.add("t", "", conversation.ModelResponded{Response: "r1"})
	l.add("t", "c1", conversation.ToolCall{Tool: "read_file", Response: "r1"})
	l.add("t", "c2", conversation.ToolCall{Tool: "run_command", Response: "r1"})
	l.add("t", "c2", conversation.ApprovalAsked{Approval: "a"})
	if _, ok := Next(l.v, full()).(Wait); !ok {
		t.Fatalf("waiting on an approval: %#v", Next(l.v, full()))
	}
	l.add("t", "c2", conversation.Decided{Decision: conversation.Accept})
	if a, ok := Next(l.v, full()).(RunTools); !ok || len(a.Calls) != 2 {
		t.Fatalf("two calls that may run: %#v", Next(l.v, full()))
	}
	for _, c := range []string{"c1", "c2"} {
		l.add("t", c, conversation.ToolStarted{})
		l.add("t", c, conversation.ToolResult{Output: []byte(`"ok"`)})
	}
	if _, ok := Next(l.v, full()).(CallModel); !ok {
		t.Fatalf("after the results: %#v", Next(l.v, full()))
	}
	l.add("t", "", conversation.ModelResponded{Response: "r2"})
	l.add("t", "a", conversation.AgentMessage{Text: "done", Response: "r2"})
	if _, ok := Next(l.v, full()).(Finish); !ok {
		t.Fatalf("after the answer: %#v", Next(l.v, full()))
	}
}

func TestADeclinedCallStopsToolUseAndTheModelIsToldOnce(t *testing.T) {
	l := started(t)
	l.add("t", "", conversation.ModelResponded{Response: "r1"})
	l.add("t", "c1", conversation.ToolCall{Tool: "run_command", Response: "r1"})
	l.add("t", "c2", conversation.ToolCall{Tool: "read_file", Response: "r1"})
	l.add("t", "c1", conversation.ApprovalAsked{Approval: "a"})
	l.add("t", "c1", conversation.Decided{Decision: conversation.Decline})
	skip, ok := Next(l.v, full()).(SkipTools)
	if !ok || len(skip.Calls) != 1 || skip.Calls[0].ID != "c2" || !strings.Contains(skip.Reason, "declined run_command") {
		t.Fatalf("the call after a decline: %#v", Next(l.v, full()))
	}
	l.add("t", "c2", conversation.ToolResult{Error: "skipped: " + skip.Reason})
	a, ok := Next(l.v, full()).(CallModel)
	if !ok || !a.NoTools || a.Notice == nil || !strings.Contains(a.Notice.Text, "the user declined run_command") {
		t.Fatalf("after a decline: %#v", Next(l.v, full()))
	}
	l.add("t", "n", *a.Notice)
	if a, ok := Next(l.v, full()).(CallModel); !ok || !a.NoTools || a.Notice != nil {
		t.Errorf("told already: %#v", Next(l.v, full()))
	}
	l.add("t", "m2", conversation.UserMessage{Text: "try another way"})
	if a, ok := Next(l.v, full()).(CallModel); !ok || a.NoTools {
		t.Errorf("after new input: %#v", Next(l.v, full()))
	}
}

func TestTheBudgetStopsToolUse(t *testing.T) {
	r := full()
	r.MaxConsecutiveFailures = 2
	l := started(t)
	for i := range 2 {
		resp := fmt.Sprint("r", i)
		l.add("t", "", conversation.ModelResponded{Response: resp})
		call := fmt.Sprint("c", i)
		l.add("t", call, conversation.ToolCall{Tool: "read_file", Response: resp})
		l.add("t", call, conversation.ToolStarted{})
		l.add("t", call, conversation.ToolResult{Error: "no such file"})
	}
	if a, ok := Next(l.v, r).(CallModel); !ok || a.Notice == nil || !strings.Contains(a.Notice.Text, "2 consecutive invalid or failed tool calls") {
		t.Errorf("after two failures: %#v", Next(l.v, r))
	}
	r = full()
	r.MaxModelCalls = 2
	if a, ok := Next(l.v, r).(CallModel); !ok || a.Notice == nil || !strings.Contains(a.Notice.Text, "model call limit (2)") {
		t.Errorf("after two responses: %#v", Next(l.v, r))
	}
}

func TestTheMainAgentFitsTheWindow(t *testing.T) {
	if Main(8_000) != CompactMain || Main(200_000) != FullMain {
		t.Error("the main agent does not follow the window")
	}
	for _, name := range Names {
		if r, ok := RoleOf(name); !ok || r.Name != name || r.Rules == "" {
			t.Errorf("role %s: %+v", name, r)
		}
	}
}

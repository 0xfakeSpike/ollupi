package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

type echoTools struct{}

func (echoTools) Name() (string, string) { return "echo", "Echoes." }

func (echoTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{Name: "echo", InputSchema: map[string]any{"type": "object"}, Annotations: &tools.Annotations{ReadOnlyHint: true}}}, nil
}

func (echoTools) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{}, nil
}

// state is a turn of the full main agent that asked "hi", with the items after it.
func state(t *testing.T, items ...repo.ItemRow) State {
	t.Helper()
	registry, err := tools.New(context.Background(), nil, echoTools{})
	if err != nil {
		t.Fatal(err)
	}
	checker, err := permission.New([]permission.Layer{{Of: "test", Settings: permission.Settings{ApprovalPolicy: string(permission.OnRequest)}}}, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := role.Of(role.FullMain)
	all := append([]repo.ItemRow{{ID: "ask", TurnID: "turn", Seq: 1, Type: repo.ItemUserMessage, Status: repo.ItemCompleted, Text: "hi"}}, items...)
	for i := range all {
		all[i].Seq = i + 1
	}
	return State{
		Turn: repo.TurnRow{ID: "turn"}, Role: full, Window: contextmanager.New("m", llm.Limits{Context: 20000, Output: 2000}),
		Permissions: checker, Items: all, Inspections: map[string]Inspection{}, MemoryOff: true,
	}
}

func call(id, tool string) repo.ItemRow {
	return repo.ItemRow{ID: id, TurnID: "turn", Type: repo.ItemToolCall, Status: repo.ItemPending, ResponseID: "r1", Tool: tool, Arguments: "{}", Annotations: &tools.Annotations{ReadOnlyHint: true}}
}

func next(t *testing.T, s State) Step {
	t.Helper()
	step, err := Next(s)
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// apply records the items as the run would.
func apply(s State, records ...repo.ItemRow) State {
	s.Items = append([]repo.ItemRow(nil), s.Items...)
	for _, it := range records {
		i := len(s.Items)
		for j := range s.Items {
			if s.Items[j].ID == it.ID {
				i = j
			}
		}
		if i == len(s.Items) {
			it.Seq = len(s.Items) + 1
			s.Items = append(s.Items, it)
		} else {
			it.Seq = s.Items[i].Seq
			s.Items[i] = it
		}
	}
	return s
}

func TestARunStartsByAskingTheModel(t *testing.T) {
	s := state(t)
	s.MemoryOff = false
	if _, ok := next(t, s).(LoadMemory); !ok {
		t.Fatal("the full main agent did not load memory first")
	}
	step, ok := next(t, state(t)).(CallModel)
	if !ok || step.Choice != llm.ToolChoiceAuto || step.Prompted != 1 {
		t.Fatalf("then: %#v", step)
	}
}

func TestACallIsInspectedBeforeItRuns(t *testing.T) {
	s := state(t, call("c", "echo"))
	inspect, ok := next(t, s).(Inspect)
	if !ok || inspect.Call.ID != "c" || !inspect.Prepare {
		t.Fatalf("a pending call: %#v", inspect)
	}
	s.Inspections["c"] = Inspection{}
	calls, ok := next(t, s).(Calls)
	if !ok || len(calls.Records) != 0 || len(calls.Run) != 1 || calls.Run[0].it.ID != "c" {
		t.Fatalf("once inspected: %#v", calls)
	}
}

func TestAnUnknownToolIsRefused(t *testing.T) {
	calls, ok := next(t, state(t, call("c", "nowhere"))).(Calls)
	if !ok || len(calls.Run) != 0 || len(calls.Records) != 1 || calls.Records[0].Status != repo.ItemFailed ||
		!strings.Contains(calls.Records[0].Error, "not available") {
		t.Fatalf("an unknown tool: %#v", calls)
	}
}

func TestADeclinedCallStopsToolUseUntilTheModelAnswers(t *testing.T) {
	declined := call("c", "echo")
	declined.Approval = &repo.Approval{ID: "a", Decision: repo.DecisionDecline}
	s := state(t, declined)
	calls, ok := next(t, s).(Calls)
	if !ok || len(calls.Records) != 1 || calls.Records[0].Status != repo.ItemDeclined {
		t.Fatalf("a declined call: %#v", calls)
	}
	s = apply(s, calls.Records...)
	stop, ok := next(t, s).(Stop)
	if !ok || !strings.Contains(stop.Notice.Text, "the user declined echo") {
		t.Fatalf("after it: %#v", stop)
	}
	s = apply(s, stop.Notice)
	ask, ok := next(t, s).(CallModel)
	if !ok || ask.Choice != llm.ToolChoiceNone || ask.Stopped != "the user declined echo" {
		t.Fatalf("after the notice: %#v", ask)
	}
	s.Prompted = ask.Prompted
	s = apply(s, repo.ItemRow{ID: "answer", TurnID: "turn", Type: repo.ItemAgentMessage, Status: repo.ItemCompleted, ResponseID: "r2", Text: "I could not."})
	if _, ok := next(t, s).(Done); !ok {
		t.Fatal("the answer did not end the run")
	}
}

func TestTheFinalAnswerEndsTheRunUnlessInputCameMeanwhile(t *testing.T) {
	s := state(t, repo.ItemRow{ID: "answer", TurnID: "turn", Type: repo.ItemAgentMessage, Status: repo.ItemCompleted, ResponseID: "r1", Text: "hello"})
	if _, ok := next(t, s).(CallModel); !ok {
		t.Fatal("a run that has not asked yet did not ask")
	}
	s.Prompted = 1
	if _, ok := next(t, s).(Done); !ok {
		t.Fatal("the answer did not end the run")
	}
	s = apply(s, repo.ItemRow{ID: "more", TurnID: "turn", Type: repo.ItemUserMessage, Status: repo.ItemCompleted, Text: "and?"})
	if _, ok := next(t, s).(CallModel); !ok {
		t.Fatal("a message that came while the model answered was not answered")
	}
}

func TestCallsThatFailInARowStopToolUse(t *testing.T) {
	var failed []repo.ItemRow
	for i := range maxConsecutiveFailures {
		it := call(string(rune('a'+i)), "echo")
		it.Status, it.Error = repo.ItemFailed, "broken"
		failed = append(failed, it)
	}
	stop, ok := next(t, state(t, failed...)).(Stop)
	if !ok || !strings.Contains(stop.Notice.Text, "10 consecutive invalid or failed tool calls; last: broken") {
		t.Fatalf("after %d failures: %#v", maxConsecutiveFailures, stop)
	}
	if _, ok := next(t, state(t, failed[1:]...)).(CallModel); !ok {
		t.Fatal("fewer failures stopped tool use")
	}
}

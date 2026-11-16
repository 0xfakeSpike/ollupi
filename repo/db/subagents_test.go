package db

import (
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/google/uuid"
)

// subagentShard is a store with a conversation whose turn is in progress, for launching subagents from it.
type subagentShard struct {
	t     *testing.T
	db    *DB
	at    time.Time
	model llm.Model
	turn  TurnRow
}

func newSubagentShard(t *testing.T) *subagentShard {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := &subagentShard{t: t, db: store, at: time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC), model: llm.Model{Name: "m", Connection: "c"}}
	if err := store.CreateThread(ThreadRow{UserID: DefaultUser, ID: "main", Title: "main", CreatedAt: s.at, UpdatedAt: s.at}); err != nil {
		t.Fatal(err)
	}
	if _, s.turn, _, _, err = store.SubmitTurn("main", TurnRow{ID: "asked", Model: s.model, Agent: "full-main-agent", StartedAt: s.at}, "go", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Launch(s.turn.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

// launch records a completed launch call in the turn, and the subagent it writes.
func (s *subagentShard) launch(call string) ThreadRow {
	s.t.Helper()
	if _, _, err := s.db.RecordItem("main", ItemRow{ID: call, TurnID: s.turn.ID, Type: ItemToolCall, Status: ItemCompleted, Tool: "general-sub-agent",
		CallID: "c-" + call, Arguments: "{}", Result: &ToolResult{}, StartedAt: s.at}); err != nil {
		s.t.Fatal(err)
	}
	child, err := s.db.WriteSubagent("main", call, "task "+call, TurnRow{Agent: "general-sub-agent"}, "do "+call)
	if err != nil {
		s.t.Fatal(err)
	}
	return child
}

// answer records a final answer of the thread's turn.
func (s *subagentShard) answer(threadID, turnID, text string) {
	s.t.Helper()
	if _, _, err := s.db.RecordItem(threadID, ItemRow{ID: uuid.NewString(), TurnID: turnID, Type: ItemAgentMessage, Status: ItemCompleted, Text: text, StartedAt: s.at}); err != nil {
		s.t.Fatal(err)
	}
}

// end runs the subagent's turn to its end with status, and returns the report it wrote.
func (s *subagentShard) end(child ThreadRow, status TurnStatus) *SubagentReport {
	s.t.Helper()
	turn, err := s.db.ActiveTurn(child.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.db.Launch(turn.ID); err != nil {
		s.t.Fatal(err)
	}
	if status == TurnCompleted {
		s.answer(child.ID, turn.ID, "found it in "+child.Title)
	} else {
		turn.Error = "it broke"
	}
	turn.Status = status
	_, _, _, report, err := s.db.EndTurn(child.ID, turn)
	if err != nil {
		s.t.Fatal(err)
	}
	return report
}

func (s *subagentShard) status() TurnStatus {
	s.t.Helper()
	turn, err := s.db.Turn(s.turn.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	return turn.Status
}

// A turn that launched subagents answers, waits for them rather than ending, and answers the user meanwhile: a
// message makes it ready, and it waits again once it answered. Each subagent's end writes its report into the turn,
// and the last of them makes the waiting turn ready, to end once it answered with no subagent running.
func TestSubagentsReportToTheTurnThatLaunchedThem(t *testing.T) {
	s := newSubagentShard(t)
	first, second := s.launch("one"), s.launch("two")
	if running, err := s.db.RunningSubagents(s.turn.ID); err != nil || len(running) != 2 {
		t.Fatalf("running subagents %v (%v), want both", running, err)
	}
	s.answer("main", s.turn.ID, "launched both")
	if next, err := s.db.AfterAnswer(s.turn.ID); err != nil || next != TurnWaitingOnAgent {
		t.Fatalf("after the answer: %s (%v), want waitingOnAgent", next, err)
	}

	if _, closed, err := s.db.Steer("main", NewUserMessage("steer", s.turn.ID, "how is it going?", nil, s.at)); err != nil || len(closed) != 0 {
		t.Fatalf("a message to the waiting turn: closed %v (%v)", closed, err)
	}
	if got := s.status(); got != TurnReady {
		t.Fatalf("after the message the turn is %s, want ready", got)
	}
	if err := s.db.Launch(s.turn.ID); err != nil {
		t.Fatal(err)
	}
	s.answer("main", s.turn.ID, "still working on it")
	if next, err := s.db.AfterAnswer(s.turn.ID); err != nil || next != TurnWaitingOnAgent {
		t.Fatalf("after answering the message: %s (%v), want waitingOnAgent", next, err)
	}

	report := s.end(first, TurnCompleted)
	if report == nil || report.Turn.ID != s.turn.ID || report.Item.Type != ItemAgentReport || report.Item.Status != ItemCompleted ||
		report.Item.CallID != "c-one" || report.Item.Text != "task one" || report.Item.Result == nil {
		t.Fatalf("the first report: %+v", report)
	}
	if got := s.status(); got != TurnWaitingOnAgent {
		t.Fatalf("with a subagent still running the turn is %s, want waitingOnAgent", got)
	}
	report = s.end(second, TurnFailed)
	if report == nil || report.Item.Status != ItemFailed || report.Item.Error != "the subagent failed: it broke" || report.Turn.Status != TurnReady {
		t.Fatalf("the last report: %+v", report)
	}
	if got := s.status(); got != TurnReady {
		t.Fatalf("after the last report the turn is %s, want ready", got)
	}

	if err := s.db.Launch(s.turn.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.db.RunItems("main", s.turn.Seq)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, it := range items {
		if it.Type == ItemAgentReport {
			reports++
		}
	}
	if reports != 2 {
		t.Errorf("the run reads %d reports, want 2", reports)
	}
	s.answer("main", s.turn.ID, "both done")
	if next, err := s.db.AfterAnswer(s.turn.ID); err != nil || next != TurnCompleted {
		t.Fatalf("after the last answer: %s (%v), want completed", next, err)
	}
}

// A subagent that ends after the turn that launched it ended reports to no one.
func TestASubagentOfAnEndedTurnReportsToNoOne(t *testing.T) {
	s := newSubagentShard(t)
	child := s.launch("one")
	s.turn.Status, s.turn.Error = TurnInterrupted, "stopped"
	if _, _, _, _, err := s.db.EndTurn("main", s.turn); err != nil {
		t.Fatal(err)
	}
	if report := s.end(child, TurnInterrupted); report != nil {
		t.Errorf("the subagent reported to an ended turn: %+v", report)
	}
}

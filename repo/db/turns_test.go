package db

import (
	"github.com/0xfakeSpike/ollupi/tools/loader"
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestRunItemsCarryOnlyWhatOutlastsACompaction(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.createTurn(TurnRow{ID: "turn", ThreadID: "t", Seq: 1, Status: TurnInProgress, Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	call := func(id, tool string, status ItemStatus) ItemRow {
		return ItemRow{ID: id, Type: ItemToolCall, Status: status, Tool: tool, Arguments: "{}", Result: &ToolResult{}}
	}
	skill, session := call("skill", "activate_skill", ItemCompleted), call("allowed", "touch", ItemFailed)
	skill.Result.Artifacts = []tools.ContextArtifact{{Kind: tools.ArtifactAgentSkill, Name: "s", Content: "use it"}}
	skill.Native = []byte(`{"response":"with only calls"}`)
	session.Approval = &Approval{ID: "a", RequestedAt: at, Decision: DecisionAcceptForSession}
	items := []ItemRow{
		{ID: "ask", Type: ItemUserMessage, Status: ItemCompleted, Text: "old ask"},
		{ID: "answer", Type: ItemAgentMessage, Status: ItemCompleted, Text: "old answer", Native: []byte(`{}`)},
		call("plain", "run_command", ItemCompleted),
		skill,
		call("load", loader.Tool, ItemCompleted),
		session,
		{ID: "compaction", Type: ItemContextCompaction, Status: ItemCompleted, Text: "summary"},
		{ID: "recent", Type: ItemAgentMessage, Status: ItemCompleted, Text: "new answer", Native: []byte(`{"kept":true}`)},
	}
	for _, it := range items {
		it.TurnID, it.StartedAt = "turn", at
		if _, _, err := db.saveItemOf("t", it); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.RunItems("t", 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	if want := []string{"ask", "skill", "load", "allowed", "compaction", "recent"}; !slices.Equal(ids, want) {
		t.Fatalf("run items %v, want %v", ids, want)
	}
	if got[len(got)-1].Native == nil || got[1].Native != nil {
		t.Errorf("native records: recent %q, carried %q; want only items after the compaction to keep theirs", got[len(got)-1].Native, got[1].Native)
	}
}

// A turn holds a slot only while it runs: waiting on the user takes it out of the ready threads,
// and the answer puts it back, as does a restart that finds it still marked as running.
func TestOnlyReadyTurnsWaitForASlot(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	_, turn, _, _, err := db.SubmitTurn("t", TurnRow{ID: "turn", Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at}, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	ready := func(want ...string) {
		t.Helper()
		got, err := db.ReadyThreads(10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("ready threads %v, want %v", got, want)
		}
	}
	status := func(want TurnStatus) {
		t.Helper()
		got, err := db.Turn(turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want {
			t.Fatalf("turn is %s, want %s", got.Status, want)
		}
	}
	if turn.Status != TurnReady {
		t.Fatalf("a submitted turn is %s, want ready", turn.Status)
	}
	ready("t")
	if err := db.Launch(turn.ID); err != nil {
		t.Fatal(err)
	}
	ready()
	ask := ItemRow{ID: "ask", TurnID: turn.ID, Type: ItemToolCall, Status: ItemInProgress, Tool: "request_user_input", Arguments: "{}",
		Await: &tools.Await{By: tools.AwaitUser, Request: []byte(`{}`)}, StartedAt: at}
	if _, _, err := db.RecordItem("t", ask); err != nil {
		t.Fatal(err)
	}
	status(TurnInProgress)
	if got, err := db.Park(turn.ID); err != nil || got != TurnWaitingOnUserInput {
		t.Fatalf("parked as %s (%v), want waitingOnUserInput", got, err)
	}
	ready()
	ask.Status, ask.Result = ItemCompleted, &ToolResult{}
	if _, _, err := db.RecordItem("t", ask); err != nil {
		t.Fatal(err)
	}
	status(TurnReady)
	ready("t")

	if err := db.Launch(turn.ID); err != nil {
		t.Fatal(err)
	}
	stranded, err := db.StrandedThreads()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stranded, []string{"t"}) {
		t.Fatalf("stranded threads %v, want [t]", stranded)
	}
	if _, err := db.Park(turn.ID); err != nil {
		t.Fatal(err)
	}
	ready("t")
}

// A turn submitted behind another is written ready with its message, but only the thread's first open turn runs: the
// later one takes no slot while the earlier waits on the user, and its message joins the conversation after all of the
// earlier turn, though it was written before that turn's last items. The messages sent after it join it, to be answered
// together, and it takes the model of the latest.
func TestATurnBehindAnotherWaitsWithItsMessage(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	model := llm.Model{Name: "m", Connection: "c"}
	_, first, _, how, err := db.SubmitTurn("t", TurnRow{ID: "first", Model: model, StartedAt: at}, "one", nil)
	if err != nil || how != Started {
		t.Fatalf("the first turn: %v (%v), want started", how, err)
	}
	if err := db.Launch(first.ID); err != nil {
		t.Fatal(err)
	}
	ask := ItemRow{ID: "ask", TurnID: first.ID, Type: ItemToolCall, Status: ItemInProgress, Tool: "request_user_input", Arguments: "{}",
		Await: &tools.Await{By: tools.AwaitUser, Request: []byte(`{}`)}, StartedAt: at}
	if _, _, err := db.RecordItem("t", ask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Park(first.ID); err != nil {
		t.Fatal(err)
	}

	_, second, message, how, err := db.SubmitTurn("t", TurnRow{ID: "second", Model: model, StartedAt: at}, "two", nil)
	if err != nil || how != Queued || second.Status != TurnReady || message.TurnID != second.ID || message.Text != "two" {
		t.Fatalf("the turn behind: %v, status %s, message %+v (%v)", how, second.Status, message, err)
	}
	other := llm.Model{Name: "m2", Connection: "c"}
	_, joined, third, how, err := db.SubmitTurn("t", TurnRow{ID: "third", Model: other, Agent: "compact-main-agent", StartedAt: at}, "three", nil)
	if err != nil || how != Joined || joined.ID != second.ID || third.TurnID != second.ID {
		t.Fatalf("a message behind the turn behind: %v, turn %s, message %+v (%v); want it in the turn behind", how, joined.ID, third, err)
	}
	if stored, err := db.Turn(second.ID); err != nil || stored.Model != other || stored.Agent != "compact-main-agent" {
		t.Fatalf("the turn behind after the message joined: %+v (%v); want the latest message's model and role", stored, err)
	}
	if n, err := db.QueuedMessages("t"); err != nil || n != 2 {
		t.Fatalf("queued messages %d (%v), want 2", n, err)
	}
	if ready, err := db.ReadyThreads(10, nil); err != nil || len(ready) != 0 {
		t.Fatalf("ready threads %v (%v) while the current turn waits on the user; want none", ready, err)
	}
	if err := db.Launch(second.ID); err == nil {
		t.Fatal("the turn behind took a slot ahead of the current one")
	}
	if current, err := db.ActiveTurn("t"); err != nil || current.ID != first.ID {
		t.Fatalf("current turn %s (%v), want the first", current.ID, err)
	}

	ask.Status, ask.Result = ItemCompleted, &ToolResult{}
	if _, _, err := db.RecordItem("t", ask); err != nil {
		t.Fatal(err)
	}
	if err := db.Launch(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RecordItem("t", ItemRow{ID: "answer", TurnID: first.ID, Type: ItemAgentMessage, Status: ItemCompleted, Text: "done", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	ids := func(upTo int) []string {
		t.Helper()
		items, err := db.RunItems("t", upTo)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out
	}
	if got := ids(first.Seq); slices.Contains(got, message.ID) {
		t.Fatalf("the first turn's run reads %v, with the message of the turn behind it", got)
	}
	first.Status = TurnCompleted
	if _, _, _, _, err := db.EndTurn("t", first); err != nil {
		t.Fatal(err)
	}
	if current, err := db.ActiveTurn("t"); err != nil || current.ID != second.ID {
		t.Fatalf("current turn after the first ended: %s (%v), want the second", current.ID, err)
	}
	if ready, err := db.ReadyThreads(10, nil); err != nil || !slices.Equal(ready, []string{"t"}) {
		t.Fatalf("ready threads %v (%v), want [t]", ready, err)
	}
	got := ids(second.Seq)
	if len(got) != 5 || got[2] != "answer" || got[3] != message.ID || got[4] != third.ID {
		t.Fatalf("the second turn's run reads %v; want the first turn's message, call and answer, then its own two messages", got)
	}
	if n, err := db.QueuedMessages("t"); err != nil || n != 0 {
		t.Fatalf("queued messages %d (%v) once their turn is current, want 0", n, err)
	}
	page, _, err := db.ThreadPage(DefaultUser, "t", 0, 10)
	if err != nil || len(page.Items) != 5 || page.Items[4].ID != third.ID {
		t.Fatalf("the page ends with %+v (%v); want the second turn's messages last", page.Items, err)
	}
	if before, _, err := db.ThreadPage(DefaultUser, "t", message.Seq, 10); err != nil || len(before.Items) != 3 || before.Items[2].ID != "answer" {
		t.Fatalf("the page before the second turn's message: %+v (%v); want all of the first turn", before.Items, err)
	}
}

// A scheduled task's turn takes no one's messages and joins no one's turn: a message behind it starts a turn of its
// own, which the next message joins, and a task's turn behind that is a turn of its own again.
func TestAScheduledTurnJoinsNoMessages(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	model := llm.Model{Name: "m", Connection: "c"}
	submit := func(id, job string, want Submission) TurnRow {
		t.Helper()
		_, turn, _, how, err := db.SubmitTurn("t", TurnRow{ID: id, Model: model, CronJobID: job, StartedAt: at}, id, nil)
		if err != nil || how != want {
			t.Fatalf("message %s: %v (%v), want %v", id, how, err, want)
		}
		return turn
	}
	submit("now", "", Started)
	task := submit("task", "standup", Queued)
	asked := submit("asked", "", Queued)
	if again := submit("again", "", Joined); again.ID != asked.ID {
		t.Fatalf("the second message joined turn %s, want %s", again.ID, asked.ID)
	}
	if later := submit("later", "standup", Queued); later.ID == asked.ID || later.ID == task.ID {
		t.Fatalf("the task's turn behind the user's is %s, want a turn of its own", later.ID)
	}
	if waiting, err := db.HasTurnBehind("t", "standup"); err != nil || !waiting {
		t.Fatalf("the task's run waits behind another: %v (%v), want true", waiting, err)
	}
}

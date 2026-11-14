package repo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

func TestABackgroundCallEndsOnce(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{ID: "t", UserID: DefaultUser, Title: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.StartBackgroundCall(DefaultUser, BackgroundCallRow{ID: "b", ThreadID: "t", ItemID: "call", Tool: "run_command", Arguments: `{"command":"sleep 9"}`, StartedAt: at}, 1); err != nil {
		t.Fatal(err)
	}
	ended, err := db.EndBackgroundCall("b", BackgroundCompleted, &ToolResult{Output: json.RawMessage(`"done"`)}, "", at.Add(time.Minute))
	if err != nil || !ended {
		t.Fatalf("ending the running call: %v %v", ended, err)
	}
	if again, err := db.EndBackgroundCall("b", BackgroundCancelled, nil, "cancelled", at.Add(2*time.Minute)); err != nil || again {
		t.Errorf("an ended call ended again: %v %v", again, err)
	}
	call, err := db.BackgroundCall("b")
	if err != nil || call.Status != BackgroundCompleted || call.Result == nil || string(call.Result.Output) != `"done"` || call.CompletedAt == nil {
		t.Errorf("the call after it ended: %+v %v", call, err)
	}
}

func TestAUserRunsAtMostSoManyBackgroundCalls(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{ID: "t", UserID: DefaultUser, Title: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	start := func(id string) error {
		return db.StartBackgroundCall(DefaultUser, BackgroundCallRow{ID: id, ThreadID: "t", ItemID: id, Tool: "run_command", StartedAt: at}, 2)
	}
	if err := start("a"); err != nil {
		t.Fatal(err)
	}
	if err := start("b"); err != nil {
		t.Fatal(err)
	}
	if err := start("c"); err == nil {
		t.Fatal("a third call started past the limit of two")
	}
	if running, err := db.RunningBackgroundCalls("t"); err != nil || len(running) != 2 {
		t.Errorf("running: %q %v", running, err)
	}
	interrupted, err := db.InterruptBackgroundCalls(at.Add(time.Hour))
	if err != nil || len(interrupted) != 2 {
		t.Fatalf("interrupted: %d %v", len(interrupted), err)
	}
	if call, err := db.BackgroundCall("a"); err != nil || call.Status != BackgroundInterrupted {
		t.Errorf("a call of a stopped process: %+v %v", call, err)
	}
	if err := start("c"); err != nil {
		t.Errorf("a call after the others ended: %v", err)
	}
}

func TestEndedBackgroundCallsComeBackToTheirThreadOnce(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	model := llm.Model{Name: "m", Connection: "c"}
	for _, id := range []string{"idle", "busy", "sub"} {
		if err := db.CreateThread(ThreadRow{ID: id, UserID: DefaultUser, Title: id, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	end := func(thread, id string, status BackgroundStatus) {
		t.Helper()
		if err := db.StartBackgroundCall(DefaultUser, BackgroundCallRow{ID: id, ThreadID: thread, ItemID: id, Tool: "run_command", StartedAt: at}, 10); err != nil {
			t.Fatal(err)
		}
		if _, err := db.EndBackgroundCall(id, status, &ToolResult{Output: json.RawMessage(`"out of ` + id + `"`)}, "", at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	end("idle", "done", BackgroundCompleted)
	end("idle", "dropped", BackgroundCancelled)
	end("busy", "late", BackgroundCompleted)
	end("sub", "orphan", BackgroundInterrupted)

	if threads, err := db.UndeliveredBackgroundThreads(); err != nil || len(threads) != 3 {
		t.Fatalf("threads with results to deliver: %q %v", threads, err)
	}
	next := &TurnRow{ID: "woken", Model: model, Agent: "full-main-agent"}
	_, turn, items, started, err := db.DeliverBackgroundCalls("idle", next)
	if err != nil || !started || turn.ID != "woken" || turn.Status != TurnReady || len(items) != 1 || items[0].BackgroundID != "done" || items[0].Type != ItemBackgroundResult {
		t.Fatalf("an idle thread: started %v turn %+v items %+v (%v)", started, turn, items, err)
	}
	if _, _, again, _, err := db.DeliverBackgroundCalls("idle", next); err != nil || len(again) != 0 {
		t.Errorf("a result came back twice: %d %v", len(again), err)
	}

	if _, _, _, err := db.SubmitTurn("busy", TurnRow{ID: "asked", Model: model, Agent: "full-main-agent", Input: "go", CreatedAt: at, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.saveItemOf("busy", ItemRow{ID: "answer", TurnID: "asked", Type: ItemAgentMessage, Status: ItemCompleted, Text: "done", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	_, turn, items, started, err = db.DeliverBackgroundCalls("busy", &TurnRow{ID: "unused", Model: model, Agent: "full-main-agent"})
	if err != nil || started || turn.ID != "asked" || len(items) != 1 {
		t.Fatalf("a thread with a turn: started %v turn %s items %d (%v)", started, turn.ID, len(items), err)
	}
	if more, err := db.InputAfterAnswer("asked"); err != nil || !more {
		t.Errorf("a result after the answer is no input after it: %v %v", more, err)
	}

	if _, _, items, started, err := db.DeliverBackgroundCalls("sub", nil); err != nil || started || len(items) != 0 {
		t.Errorf("a thread that takes no new turn: started %v items %d (%v)", started, len(items), err)
	}
	if threads, err := db.UndeliveredBackgroundThreads(); err != nil || len(threads) != 0 {
		t.Errorf("results left to deliver: %q %v", threads, err)
	}
}

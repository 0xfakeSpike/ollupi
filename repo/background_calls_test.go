package repo

import (
	"encoding/json"
	"testing"
	"time"
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

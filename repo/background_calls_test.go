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
	if err := db.StartBackgroundCall(BackgroundCallRow{ID: "b", ThreadID: "t", ItemID: "call", Tool: "run_command", Arguments: `{"command":"sleep 9"}`, StartedAt: at}); err != nil {
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

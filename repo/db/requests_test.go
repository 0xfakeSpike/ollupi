package db

import (
	"errors"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// waitingOnApproval opens a database with thread "t", whose turn ran and left a call waiting for approval. It returns
// the call as it was read then.
func waitingOnApproval(t *testing.T) (*DB, ItemRow) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	at := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	if err := store.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	_, turn, _, err := store.SubmitTurn("t", TurnRow{ID: "turn", Input: "hi", Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Launch(turn.ID); err != nil {
		t.Fatal(err)
	}
	call, _, err := store.RecordItem("t", ItemRow{ID: "call", TurnID: turn.ID, Type: ItemToolCall, Status: ItemPending, Tool: "run_command",
		Arguments: "{}", Approval: &Approval{ID: "a", RequestedAt: at}, StartedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return store, call
}

func decided(call ItemRow, decision Decision) ItemRow {
	now := time.Now().UTC()
	approval := *call.Approval
	approval.Decision, approval.DecidedAt = decision, &now
	call.Approval = &approval
	return call
}

func TestARequestIsAnsweredOnlyWhileItsTurnWaitsOnIt(t *testing.T) {
	store, call := waitingOnApproval(t)
	if _, err := store.AnswerRequest("t", decided(call, DecisionAccept)); !errors.Is(err, ErrNoSuchRequest) {
		t.Fatalf("an answer while the turn runs: %v, want %v", err, ErrNoSuchRequest)
	}
	if status, err := store.Park("turn"); err != nil || status != TurnWaitingOnApproval {
		t.Fatalf("parked as %s (%v), want waiting on approval", status, err)
	}
	if _, err := store.AnswerRequest("t", decided(call, DecisionAccept)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AnswerRequest("t", decided(call, DecisionDecline)); !errors.Is(err, ErrNoSuchRequest) {
		t.Fatalf("a second answer: %v, want %v", err, ErrNoSuchRequest)
	}
	got, err := store.Item("call")
	if err != nil {
		t.Fatal(err)
	}
	if got.Approval.Decision != DecisionAccept {
		t.Errorf("the decision is %q, want the first answer's", got.Approval.Decision)
	}
}

func TestAnAnswerReadBeforeAMessageDoesNotUndoIt(t *testing.T) {
	store, call := waitingOnApproval(t)
	if _, err := store.Park("turn"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Steer("t", NewUserMessage("m", "turn", "do something else", nil, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AnswerRequest("t", decided(call, DecisionAccept)); !errors.Is(err, ErrNoSuchRequest) {
		t.Fatalf("an answer the message declined: %v, want %v", err, ErrNoSuchRequest)
	}
	got, err := store.Item("call")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ItemDeclined || got.Approval.Decision != "" {
		t.Errorf("the call is %s with decision %q, want declined by the message", got.Status, got.Approval.Decision)
	}
}

func TestABusyThreadIsNeitherArchivedNorDeleted(t *testing.T) {
	store, _ := waitingOnApproval(t)
	at := time.Now().UTC()
	if _, err := store.SetThreadArchived(DefaultUser, "t", &at); !errors.Is(err, ErrThreadBusy) {
		t.Errorf("archive: %v, want %v", err, ErrThreadBusy)
	}
	if err := store.DeleteThread(DefaultUser, "t"); !errors.Is(err, ErrThreadBusy) {
		t.Errorf("delete: %v, want %v", err, ErrThreadBusy)
	}
	if err := store.DeleteThread("someone else", "t"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete by another user: %v, want %v", err, ErrNotFound)
	}
}

func TestAnArchivedThreadTakesNoTurn(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Now().UTC()
	if err := store.CreateThread(ThreadRow{UserID: DefaultUser, ID: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetThreadArchived(DefaultUser, "t", &at); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.SubmitTurn("t", TurnRow{ID: "turn", Input: "hi", Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at}); !errors.Is(err, ErrThreadArchived) {
		t.Fatalf("submit to an archived thread: %v, want %v", err, ErrThreadArchived)
	}
}

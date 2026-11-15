package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func failingBatch(turnID string, entries int) *Batch {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &Batch{UserID: db.DefaultUser, Scope: db.MemoryGeneral, ThreadID: "thread-" + turnID, TurnID: turnID, Cursor: db.MemoryCursor{At: &at, ID: turnID, Seq: entries}, Entries: entries}
}

func memoryState(t *testing.T, m *Manager) db.MemoryStatus {
	t.Helper()
	s, err := m.Status(db.DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func succeed(t *testing.T, m *Manager, b *Batch) {
	t.Helper()
	if err := commit(m.db, b, nil, db.MemoryReport{}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryFailureClearsOnSuccess(t *testing.T) {
	m := testManager(t, false)
	b := failingBatch("t1", 3)
	cause := fmt.Errorf("extract: %w", failure{db.MemoryModelRequest, errors.New("connection refused")})
	if err := m.deferBatch(b, cause); err != nil {
		t.Fatal(err)
	}
	s := memoryState(t, m)
	if s.LastFailure == nil || s.LastFailure.Code != db.MemoryModelRequest || s.LastFailure.Error != cause.Error() {
		t.Fatalf("lastFailure %+v, want the model request failure", s.LastFailure)
	}
	if s.Attempts != 1 || s.NextAttempt == nil || !s.NextAttempt.After(time.Now()) {
		t.Fatalf("attempts %d, nextAttempt %v: want the first retry ahead", s.Attempts, s.NextAttempt)
	}
	succeed(t, m, b)
	if s := memoryState(t, m); s.LastFailure != nil || s.Attempts != 0 || s.NextAttempt != nil || s.LastSkip != nil {
		t.Fatalf("after a success: %+v, want no error, retry or skip", s.MemoryStateRow)
	}
}

func TestMemorySkipOutlivesSuccess(t *testing.T) {
	m := testManager(t, false)
	b := failingBatch("t1", 5)
	for i := 1; i < 10; i++ {
		if err := m.deferBatch(b, failure{db.MemoryModelRequest, errors.New("timeout")}); err != nil {
			t.Fatal(err)
		}
		if s := memoryState(t, m); s.LastSkip != nil || s.Attempts != i {
			t.Fatalf("failure %d: attempts %d, lastSkip %+v", i, s.Attempts, s.LastSkip)
		}
	}
	before := time.Now().UTC()
	if err := m.deferBatch(b, failure{db.MemoryInvalidResponse, errors.New("invalid JSON")}); err != nil {
		t.Fatal(err)
	}
	s := memoryState(t, m)
	skip := s.LastSkip
	if skip == nil || skip.Entries != 5 || skip.TurnID != "t1" || skip.ThreadID != "thread-t1" || skip.Code != db.MemoryInvalidResponse || skip.Error != "invalid JSON" || skip.At.Before(before) {
		t.Fatalf("lastSkip %+v, want the batch of turn t1 with its last failure", skip)
	}
	if s.LastFailure != nil || s.Attempts != 0 || s.NextAttempt != nil || s.Skipped != 5 {
		t.Fatalf("after the skip: %+v, skipped %d", s.MemoryStateRow, s.Skipped)
	}

	if err := m.deferBatch(failingBatch("t2", 2), failure{db.MemoryEvidence, errors.New("too large")}); err != nil {
		t.Fatal(err)
	}
	succeed(t, m, failingBatch("t2", 2))
	s = memoryState(t, m)
	if s.LastFailure != nil || s.LastSkip == nil || *s.LastSkip != *skip || s.Skipped != 5 {
		t.Fatalf("after a success: lastFailure %+v, lastSkip %+v, skipped %d; want the skip kept", s.LastFailure, s.LastSkip, s.Skipped)
	}
}

func TestFailureOfUnclassified(t *testing.T) {
	if got := failureOf(errors.New("disk full")); got.Code != db.MemoryStorage || got.Error != "disk full" {
		t.Fatalf("got %+v", got)
	}
}

type namedClient struct {
	name  string
	calls *[]string
}

func (c namedClient) Chat(context.Context, llm.ChatRequest, func(llm.Delta)) (llm.ChatResponse, error) {
	*c.calls = append(*c.calls, c.name)
	return llm.ChatResponse{Message: llm.ChatMessage{Content: `{"operations":[]}`}, FinishReason: "stop"}, nil
}

func (namedClient) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 32000, Output: 4000}, nil
}

func TestExtractionRunsOnTheUsersClient(t *testing.T) {
	m := testManager(t, false)
	var calls []string
	m.model = namedClient{"shared", &calls}
	m.opts.ClientFor = func(user string) (llm.Client, error) { return namedClient{user, &calls}, nil }
	if _, err := m.extract(context.Background(), scopeBatch(), "{}", &db.MemoryReport{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{db.DefaultUser}) {
		t.Errorf("calls went through %v", calls)
	}
	m.opts.ClientFor = func(string) (llm.Client, error) { return nil, errors.New("no key") }
	_, err := m.extract(context.Background(), scopeBatch(), "{}", &db.MemoryReport{})
	if failureOf(err).Code != db.MemoryModelRequest {
		t.Errorf("no client for the user: %v", err)
	}
}

func TestACrashIsOneUsersFailure(t *testing.T) {
	m := testManager(t, false)
	m.db = nil
	if err := m.runFor(context.Background(), db.DefaultUser); err == nil {
		t.Error("the crash was not reported")
	}
}

type crashingClient struct{}

func (crashingClient) Chat(context.Context, llm.ChatRequest, func(llm.Delta)) (llm.ChatResponse, error) {
	panic("the extraction crashed")
}

func (crashingClient) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 32000, Output: 4000}, nil
}

func TestABatchThatCrashesIsPutOff(t *testing.T) {
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	if err := store.CreateThread(db.ThreadRow{ID: "t", UserID: db.DefaultUser, Title: "t", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	_, turn, _, _, err := store.SubmitTurn("t", db.TurnRow{ID: "turn", Input: "I prefer tea", Model: llm.Model{Name: "m", Connection: "c"}, Agent: "full-main-agent", CreatedAt: at, StartedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Launch(turn.ID); err != nil {
		t.Fatal(err)
	}
	turn.Status = db.TurnCompleted
	if _, _, _, err := store.EndTurn("t", turn); err != nil {
		t.Fatal(err)
	}
	m, err := New(store, crashingClient{}, Options{Model: llm.Model{Name: "m", Connection: "c"}, Generate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.runFor(context.Background(), db.DefaultUser); err != nil {
		t.Fatal(err)
	}
	state, err := store.MemoryState(db.DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	if state.Attempts != 1 || state.NextAttempt == nil || state.LastFailure == nil {
		t.Fatalf("after the crash: %d attempts, next %v, failure %v; want the batch put off", state.Attempts, state.NextAttempt, state.LastFailure)
	}
}

func TestEvidenceIsNotCutInsideAKey(t *testing.T) {
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("b3BlbnNzaC1rZXktdjEAAAAA\n", 120) + "-----END OPENSSH PRIVATE KEY-----"
	clipped := clipMemory("cat ~/.ssh/id_rsa\n"+key, 3000)
	if strings.Contains(clipped, "b3BlbnNzaC1rZXktdjEAAAAA") {
		t.Errorf("the evidence keeps part of the key, which masking no longer finds:\n%.200s", clipped)
	}
}

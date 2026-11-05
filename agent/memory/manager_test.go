package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
)

func failingBatch(turnID string, entries int) *Batch {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &Batch{UserID: repo.DefaultUser, Scope: repo.MemoryGeneral, ThreadID: "thread-" + turnID, TurnID: turnID, Cursor: repo.MemoryCursor{At: &at, ID: turnID, Seq: entries}, Entries: entries}
}

func memoryState(t *testing.T, m *Manager) repo.MemoryStatus {
	t.Helper()
	s, err := m.Status(repo.DefaultUser)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func succeed(t *testing.T, m *Manager, b *Batch) {
	t.Helper()
	if err := commit(m.db, b, nil, repo.MemoryReport{}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryFailureClearsOnSuccess(t *testing.T) {
	m := testManager(t, false)
	b := failingBatch("t1", 3)
	cause := fmt.Errorf("extract: %w", failure{repo.MemoryModelRequest, errors.New("connection refused")})
	if err := m.deferBatch(b, cause); err != nil {
		t.Fatal(err)
	}
	s := memoryState(t, m)
	if s.LastFailure == nil || s.LastFailure.Code != repo.MemoryModelRequest || s.LastFailure.Error != cause.Error() {
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
		if err := m.deferBatch(b, failure{repo.MemoryModelRequest, errors.New("timeout")}); err != nil {
			t.Fatal(err)
		}
		if s := memoryState(t, m); s.LastSkip != nil || s.Attempts != i {
			t.Fatalf("failure %d: attempts %d, lastSkip %+v", i, s.Attempts, s.LastSkip)
		}
	}
	before := time.Now().UTC()
	if err := m.deferBatch(b, failure{repo.MemoryInvalidResponse, errors.New("invalid JSON")}); err != nil {
		t.Fatal(err)
	}
	s := memoryState(t, m)
	skip := s.LastSkip
	if skip == nil || skip.Entries != 5 || skip.TurnID != "t1" || skip.ThreadID != "thread-t1" || skip.Code != repo.MemoryInvalidResponse || skip.Error != "invalid JSON" || skip.At.Before(before) {
		t.Fatalf("lastSkip %+v, want the batch of turn t1 with its last failure", skip)
	}
	if s.LastFailure != nil || s.Attempts != 0 || s.NextAttempt != nil || s.Skipped != 5 {
		t.Fatalf("after the skip: %+v, skipped %d", s.MemoryStateRow, s.Skipped)
	}

	if err := m.deferBatch(failingBatch("t2", 2), failure{repo.MemoryEvidence, errors.New("too large")}); err != nil {
		t.Fatal(err)
	}
	succeed(t, m, failingBatch("t2", 2))
	s = memoryState(t, m)
	if s.LastFailure != nil || s.LastSkip == nil || *s.LastSkip != *skip || s.Skipped != 5 {
		t.Fatalf("after a success: lastFailure %+v, lastSkip %+v, skipped %d; want the skip kept", s.LastFailure, s.LastSkip, s.Skipped)
	}
}

func TestFailureOfUnclassified(t *testing.T) {
	if got := failureOf(errors.New("disk full")); got.Code != repo.MemoryStorage || got.Error != "disk full" {
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
	if _, err := m.extract(context.Background(), scopeBatch(), "{}", &repo.MemoryReport{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{repo.DefaultUser}) {
		t.Errorf("calls went through %v", calls)
	}
	m.opts.ClientFor = func(string) (llm.Client, error) { return nil, errors.New("no key") }
	_, err := m.extract(context.Background(), scopeBatch(), "{}", &repo.MemoryReport{})
	if failureOf(err).Code != repo.MemoryModelRequest {
		t.Errorf("no client for the user: %v", err)
	}
}

func TestACrashIsOneUsersFailure(t *testing.T) {
	m := testManager(t, false)
	m.db = nil
	if err := m.runFor(context.Background(), repo.DefaultUser); err == nil {
		t.Error("the crash was not reported")
	}
}

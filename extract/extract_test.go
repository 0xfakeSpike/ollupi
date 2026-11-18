package extract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/user"
)

type model struct {
	answer func(payload string) (string, error)
	calls  int
}

func (m *model) Chat(_ context.Context, req llm.ChatRequest, _ func(llm.Delta)) (llm.ChatResponse, error) {
	m.calls++
	content, err := m.answer(req.Messages[1].Content)
	return llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: content}, FinishReason: "stop"}, err
}

func (*model) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 8_000}, nil
}

const addEditor = `{"operations":[{"action":"add","topic":"editor","kind":"preference","text":"Uses vim.","sources":[{"thread":"th","item":"m1"}]}]}`

func setup(t *testing.T, m *model) (*Extractor, store.Store) {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	x, err := New(st, llm.Model{Name: "m"}, llm.Limits{Context: 200_000, Output: 8_000}, func(string) (llm.Client, error) { return m, nil })
	if err != nil {
		t.Fatal(err)
	}
	return x, st
}

func update(t *testing.T, st store.Store, fn func(store.Tx) error) {
	t.Helper()
	if err := st.Update(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func converse(t *testing.T, st store.Store, u, th string) {
	t.Helper()
	now := time.Now().UTC()
	update(t, st, func(tx store.Tx) error {
		if err := tx.EnsureUser(u); err != nil {
			return err
		}
		if err := tx.CreateThread(store.Thread{User: u, Thread: user.Thread{ID: th, Title: "chat", Created: now, Updated: now}}); err != nil {
			return err
		}
		_, err := tx.Append(th,
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "I use vim"}},
			conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: "t1", Item: "a1", Body: conversation.AgentMessage{Text: "noted", Response: "r"}},
			conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}})
		return err
	})
}

func read[T any](t *testing.T, st store.Store, fn func(store.Reader) (T, error)) T {
	t.Helper()
	var out T
	if err := st.Read(context.Background(), func(r store.Reader) (err error) { out, err = fn(r); return err }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestATurnTeachesANoteAndIsReadOnce(t *testing.T) {
	m := &model{answer: func(payload string) (string, error) {
		if !strings.Contains(payload, "I use vim") {
			return "", errors.New("the evidence is missing")
		}
		return addEditor, nil
	}}
	x, st := setup(t, m)
	converse(t, st, "u", "th")
	if _, err := x.readAll(context.Background(), "u"); err != nil {
		t.Fatal(err)
	}
	notes := read(t, st, func(r store.Reader) ([]memory.Memory, error) { return r.Memories("u") })
	if len(notes) != 1 || notes[0].Text != "Uses vim." || notes[0].Scope != memory.General {
		t.Fatalf("notes %+v", notes)
	}
	state := read(t, st, func(r store.Reader) (memory.State, error) { return r.MemoryState("u") })
	if state.Cursor.Thread != "th" || state.LastSuccess == nil {
		t.Errorf("state %+v", state)
	}
	if _, err := x.readAll(context.Background(), "u"); err != nil || m.calls != 1 {
		t.Errorf("read again: %d calls, %v", m.calls, err)
	}
}

func TestAUserWithMemoryOffIsPassedOver(t *testing.T) {
	m := &model{answer: func(string) (string, error) { return addEditor, nil }}
	x, st := setup(t, m)
	converse(t, st, "u", "th")
	update(t, st, func(tx store.Tx) error {
		s, err := tx.Settings("u")
		if err != nil {
			return err
		}
		s.Memory = false
		return tx.SetSettings("u", s)
	})
	if _, err := x.readAll(context.Background(), "u"); err != nil {
		t.Fatal(err)
	}
	state := read(t, st, func(r store.Reader) (memory.State, error) { return r.MemoryState("u") })
	if m.calls != 0 || state.Cursor.Thread != "th" || state.LastSuccess != nil {
		t.Errorf("%d calls, state %+v", m.calls, state)
	}
}

func TestAFailedTurnIsTriedAgainLater(t *testing.T) {
	m := &model{answer: func(string) (string, error) { return "", errors.New("down") }}
	x, st := setup(t, m)
	converse(t, st, "u", "th")
	due, err := x.readAll(context.Background(), "u")
	if err != nil || !due.After(time.Now()) {
		t.Fatalf("due %v, %v", due, err)
	}
	state := read(t, st, func(r store.Reader) (memory.State, error) { return r.MemoryState("u") })
	if state.Attempts != 1 || state.LastFailure == nil || state.LastFailure.Code != memory.ModelRequest || state.Cursor.Thread != "" {
		t.Errorf("state %+v", state)
	}
	if _, err := x.readAll(context.Background(), "u"); err != nil || m.calls != 1 {
		t.Errorf("tried before it was due: %d calls, %v", m.calls, err)
	}
}

func TestTheUsersEditMeanwhileComesFirst(t *testing.T) {
	m := &model{}
	x, st := setup(t, m)
	converse(t, st, "u", "th")
	now := time.Now().UTC().Add(-time.Hour)
	kept := memory.Memory{ID: "k", User: "u", Place: memory.Place{Scope: memory.General}, Topic: "editor", Kind: memory.Preference, Text: "Uses emacs.",
		Status: memory.Active, Sources: []memory.Source{{Thread: "th", Item: "m1"}}, Created: now, Updated: now}
	update(t, st, func(tx store.Tx) error { return tx.PutMemory(kept) })
	m.answer = func(string) (string, error) {
		edited, _ := memory.Edit(kept, "Uses helix.", time.Now().UTC())
		update(t, st, func(tx store.Tx) error { return tx.PutMemory(edited) })
		return `{"operations":[{"action":"update","id":"k","topic":"editor","kind":"preference","text":"Uses vim.","sources":[{"thread":"th","item":"m1"}]}]}`, nil
	}
	if _, err := x.readAll(context.Background(), "u"); err != nil {
		t.Fatal(err)
	}
	notes := read(t, st, func(r store.Reader) ([]memory.Memory, error) { return r.Memories("u") })
	if len(notes) != 1 || notes[0].Text != "Uses helix." {
		t.Errorf("notes %+v", notes)
	}
}

func TestRunHearsATurnEnd(t *testing.T) {
	m := &model{answer: func(string) (string, error) { return addEditor, nil }}
	x, st := setup(t, m)
	learned := make(chan memory.Memory, 1)
	sub := st.Feed().Subscribe("u", func(e store.Event) {
		if e.Owned != nil && e.Owned.Memory != nil {
			learned <- *e.Owned.Memory
		}
	})
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- x.Run(ctx) }()
	converse(t, st, "u", "th")
	select {
	case n := <-learned:
		if n.Text != "Uses vim." {
			t.Errorf("learned %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn's end was not heard")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("run ended with %v", err)
	}
}

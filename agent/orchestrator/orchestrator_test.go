package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/resources"
	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

var model = llm.Model{Name: "demo", Connection: "fake"}

// fakeModel answers each chat, holding it until released when hold is set, and counts the chats running at once.
type fakeModel struct {
	hold    chan struct{}
	asked   chan string
	mu      sync.Mutex
	running int
	most    int
}

func (m *fakeModel) Chat(ctx context.Context, req llm.ChatRequest, _ func(llm.Delta)) (llm.ChatResponse, error) {
	m.mu.Lock()
	m.running++
	m.most = max(m.most, m.running)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
	}()
	if m.asked != nil {
		// The message ends with the user's input, after when it was sent.
		words := strings.Fields(req.Messages[len(req.Messages)-1].Content)
		m.asked <- words[len(words)-1]
	}
	if m.hold != nil {
		select {
		case <-m.hold:
		case <-ctx.Done():
			return llm.ChatResponse{}, context.Cause(ctx)
		}
	}
	return llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"}, FinishReason: "stop"}, nil
}

func (m *fakeModel) Limits(string) (llm.Limits, error) {
	return llm.Limits{Context: 32000, Output: 4000}, nil
}

// next is the input of the next chat to start.
func (m *fakeModel) next(t *testing.T) string {
	t.Helper()
	select {
	case input := <-m.asked:
		return input
	case <-time.After(30 * time.Second):
		t.Fatal("no chat started")
		return ""
	}
}

func (m *fakeModel) mostAtOnce() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.most
}

// shard is an orchestrator on its own database, with the turns that ended.
type shard struct {
	o     *orchestrator.Orchestrator
	store *db.DB
	res   *resources.Resources
	ended chan db.TurnRow
}

func newShard(t *testing.T, fake *fakeModel, cfg orchestrator.Config) *shard {
	t.Helper()
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	res, err := resources.New(context.Background(), resources.Config{
		Models:      []llm.Model{model},
		Connections: map[string]llm.Client{model.Connection: fake},
	}, []tools.Provider{runtime.Loader{}})
	if err != nil {
		t.Fatal(err)
	}
	s := &shard{store: store, res: res, ended: make(chan db.TurnRow, 16)}
	cfg.DB, cfg.Runtime.Name = store, "Ollupi"
	cfg.Runtime.Settings = permission.Settings{ApprovalPolicy: string(permission.Never)}
	cfg.OnEvent = func(e event.Event) {
		if e.Kind == event.TurnCompleted {
			s.ended <- e.Turn
		}
	}
	s.o = orchestrator.New(cfg)
	return s
}

func (s *shard) start(t *testing.T) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	if err := s.o.Start(ctx, s.res); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(s.o.Wait)
	t.Cleanup(stop)
}

// submit writes a thread of user with a turn that is ready to run, as the service does, and returns the turn.
func (s *shard) submit(t *testing.T, user, input string) db.TurnRow {
	t.Helper()
	if err := s.store.EnsureUser(user); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	thread := db.ThreadRow{ID: uuid.NewString(), UserID: user, Title: input, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateThread(thread); err != nil {
		t.Fatal(err)
	}
	_, turn, _, err := s.store.SubmitTurn(thread.ID, db.TurnRow{ID: uuid.NewString(), Input: input, Model: model, Agent: "full-main-agent", CreatedAt: now, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func (s *shard) endOf(t *testing.T) db.TurnRow {
	t.Helper()
	select {
	case turn := <-s.ended:
		return turn
	case <-time.After(30 * time.Second):
		t.Fatal("no turn ended")
		return db.TurnRow{}
	}
}

func TestAWokenTurnRunsToTheEnd(t *testing.T) {
	s := newShard(t, &fakeModel{}, orchestrator.Config{})
	s.start(t)
	turn := s.submit(t, db.DefaultUser, "hello")
	s.o.Wake(turn.ThreadID)
	if ended := s.endOf(t); ended.ID != turn.ID || ended.Status != db.TurnCompleted {
		t.Fatalf("ended %s as %s (%s), want %s completed", ended.ID, ended.Status, ended.Error, turn.ID)
	}
}

func TestAUserHoldsNoMoreThanItsSlots(t *testing.T) {
	fake := &fakeModel{hold: make(chan struct{}), asked: make(chan string, 8)}
	s := newShard(t, fake, orchestrator.Config{Slots: 4, UserSlots: 1})
	s.start(t)
	if err := s.store.EnsureUser("other"); err != nil {
		t.Fatal(err)
	}
	first, second := s.submit(t, db.DefaultUser, "first"), s.submit(t, db.DefaultUser, "second")
	other := s.submit(t, "other", "other")
	for _, turn := range []db.TurnRow{first, second, other} {
		s.o.Wake(turn.ThreadID)
	}
	// One turn of each user runs; the other turn of the default user waits for its user's slot.
	asked := map[string]bool{}
	for range 2 {
		asked[fake.next(t)] = true
	}
	if !asked["other"] || asked["first"] == asked["second"] {
		t.Fatalf("running: %v, want one turn of each user", asked)
	}
	select {
	case got := <-fake.asked:
		t.Fatalf("%q runs while its user's slot is held", got)
	case <-time.After(200 * time.Millisecond):
	}
	close(fake.hold)
	for range 3 {
		if ended := s.endOf(t); ended.Status != db.TurnCompleted {
			t.Fatalf("turn %s ended as %s (%s)", ended.ID, ended.Status, ended.Error)
		}
	}
	if got := fake.mostAtOnce(); got != 2 {
		t.Errorf("%d chats ran at once, want 2", got)
	}
}

func TestTheShardRunsNoMoreThanItsSlots(t *testing.T) {
	fake := &fakeModel{hold: make(chan struct{}), asked: make(chan string, 8)}
	s := newShard(t, fake, orchestrator.Config{Slots: 2})
	s.start(t)
	for _, input := range []string{"a", "b", "c"} {
		s.o.Wake(s.submit(t, db.DefaultUser, input).ThreadID)
	}
	fake.next(t)
	fake.next(t)
	select {
	case got := <-fake.asked:
		t.Fatalf("%q runs while both slots are held", got)
	case <-time.After(200 * time.Millisecond):
	}
	close(fake.hold)
	for range 3 {
		if ended := s.endOf(t); ended.Status != db.TurnCompleted {
			t.Fatalf("turn %s ended as %s (%s)", ended.ID, ended.Status, ended.Error)
		}
	}
	if got := fake.mostAtOnce(); got != 2 {
		t.Errorf("%d chats ran at once, want 2", got)
	}
}

func TestATurnFailsAtItsRunLimit(t *testing.T) {
	s := newShard(t, &fakeModel{hold: make(chan struct{})}, orchestrator.Config{RunLimit: 100 * time.Millisecond})
	s.start(t)
	turn := s.submit(t, db.DefaultUser, "slow")
	s.o.Wake(turn.ThreadID)
	ended := s.endOf(t)
	if ended.Status != db.TurnFailed || !strings.Contains(ended.Error, "time limit") {
		t.Fatalf("ended as %s (%s), want failed at the time limit", ended.Status, ended.Error)
	}
}

func TestAnInterruptEndsTheRunningTurn(t *testing.T) {
	fake := &fakeModel{hold: make(chan struct{}), asked: make(chan string, 1)}
	s := newShard(t, fake, orchestrator.Config{})
	s.start(t)
	turn := s.submit(t, db.DefaultUser, "long")
	s.o.Wake(turn.ThreadID)
	fake.next(t)
	if running, err := s.store.Turn(turn.ID); err != nil || running.Status != db.TurnInProgress {
		t.Errorf("the running turn is %s (%v), want in progress", running.Status, err)
	}
	if err := s.o.Interrupt(turn.ThreadID, "another"); !errors.Is(err, orchestrator.ErrTurnMismatch) {
		t.Errorf("interrupt of another turn: %v, want %v", err, orchestrator.ErrTurnMismatch)
	}
	if err := s.o.Interrupt(turn.ThreadID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if ended := s.endOf(t); ended.ID != turn.ID || ended.Status != db.TurnInterrupted {
		t.Fatalf("ended %s as %s, want %s interrupted", ended.ID, ended.Status, turn.ID)
	}
	if err := s.o.Interrupt(turn.ThreadID, ""); !errors.Is(err, orchestrator.ErrNoActiveTurn) {
		t.Errorf("interrupt after the end: %v, want %v", err, orchestrator.ErrNoActiveTurn)
	}
}

func TestStartRunsTheTurnsAStoppedProcessLeft(t *testing.T) {
	s := newShard(t, &fakeModel{}, orchestrator.Config{})
	ready := s.submit(t, db.DefaultUser, "ready")
	launched := s.submit(t, db.DefaultUser, "launched")
	if err := s.store.Launch(launched.ID); err != nil {
		t.Fatal(err)
	}
	s.start(t)
	ended := map[string]db.TurnStatus{}
	for range 2 {
		turn := s.endOf(t)
		ended[turn.ID] = turn.Status
	}
	if ended[ready.ID] != db.TurnCompleted || ended[launched.ID] != db.TurnCompleted {
		t.Fatalf("ended %v, want both turns completed", ended)
	}
}

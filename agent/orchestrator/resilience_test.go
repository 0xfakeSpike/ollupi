package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func started(ctx context.Context, t *testing.T, cfg orchestrator.Config, url string) (*orchestrator.Orchestrator, *db.DB) {
	t.Helper()
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg.Name, cfg.DB = "Ollupi", store
	cfg.Models = []llm.Model{{Name: "demo", Connection: "fake"}}
	cfg.Settings = permission.Settings{ApprovalPolicy: string(permission.Never)}
	cfg.Connections = map[string]llm.Client{"fake": llm.NewOpenAI(url, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})}
	ag := orchestrator.New(cfg)
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(ag.Wait)
	t.Cleanup(stop)
	return ag, store
}

func endOf(ctx context.Context, t *testing.T, ended <-chan db.TurnRow) db.TurnRow {
	t.Helper()
	select {
	case turn := <-ended:
		return turn
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return db.TurnRow{}
	}
}

// stuckMachine does not answer until released, like a sandbox that is waking up.
type stuckMachine struct {
	noteMachine
	asked   chan struct{}
	release chan struct{}
}

func (m stuckMachine) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	close(m.asked)
	<-m.release
	return m.noteMachine.Stat(ctx, path)
}

func TestASlowSandboxDoesNotHoldUpOtherThreads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &holdingModel{release: make(chan struct{})}
	close(fake.release)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	machine := stuckMachine{asked: make(chan struct{}), release: make(chan struct{})}
	ag, _ := started(ctx, t, orchestrator.Config{
		Attachments: &memoryStore{objects: map[string][]byte{}},
		BoxFor: func(string) (sandbox.Box, error) {
			return sandbox.Remote(machine, []string{"/cloud"}, "cloud"), nil
		},
	}, srv.URL)
	stuck, err := ag.StartThread(db.DefaultUser, "", "", "stuck", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ag.StartThread(db.DefaultUser, "", "", "other", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := ag.StartTurn(ctx, db.DefaultUser, stuck.ID, orchestrator.Input{Text: "read", Attachments: []orchestrator.AttachmentInput{{Path: "/cloud/note.txt"}}})
		sent <- err
	}()
	<-machine.asked

	renamed := make(chan error, 1)
	go func() {
		_, err := ag.RenameThread(db.DefaultUser, other.ID, "renamed")
		renamed <- err
	}()
	select {
	case err := <-renamed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the loop waits for the sandbox of another thread")
	}
	close(machine.release)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestACrashFailsTheTurnAndNothingElse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(&recordingModel{})
	defer srv.Close()
	ended := make(chan db.TurnRow, 1)
	ag, _ := started(ctx, t, orchestrator.Config{
		ClientFor: func(string, string) (llm.Client, error) { panic("the secret detail") },
		OnEvent: func(e orchestrator.Event) {
			if e.Kind == orchestrator.EventTurnCompleted {
				ended <- e.Turn
			}
		},
	}, srv.URL)
	thread, err := ag.StartThread(db.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	turn := endOf(ctx, t, ended)
	if turn.Status != db.TurnFailed || strings.Contains(turn.Error, "secret") {
		t.Errorf("the turn: %s %q", turn.Status, turn.Error)
	}
	if _, err := ag.RenameThread(db.DefaultUser, thread.ID, "still here"); err != nil {
		t.Errorf("after the crash: %v", err)
	}
}

func TestATurnStopsAtItsRunLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &holdingModel{release: make(chan struct{})}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	defer close(fake.release)
	ended := make(chan db.TurnRow, 1)
	ag, _ := started(ctx, t, orchestrator.Config{
		RunLimit: 200 * time.Millisecond,
		OnEvent: func(e orchestrator.Event) {
			if e.Kind == orchestrator.EventTurnCompleted {
				ended <- e.Turn
			}
		},
	}, srv.URL)
	thread, err := ag.StartThread(db.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if turn := endOf(ctx, t, ended); turn.Status != db.TurnFailed || !strings.Contains(turn.Error, "time limit") {
		t.Errorf("the turn: %s %q", turn.Status, turn.Error)
	}
}

func TestPendingRequestsReportStorageThatFails(t *testing.T) {
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ag := orchestrator.New(orchestrator.Config{DB: store})
	if got, err := ag.AllPendingRequests(db.DefaultUser); err == nil {
		t.Errorf("all requests: %+v", got)
	}
	if got, err := ag.PendingRequests(db.DefaultUser, "t"); err == nil || errors.Is(err, db.ErrNotFound) {
		t.Errorf("a thread's requests: %+v, %v", got, err)
	}
}

// cutOffModel first answers with a tool call the output limit cut off, then with an answer.
type cutOffModel struct {
	mu    sync.Mutex
	calls int
}

func (m *cutOffModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(delta map[string]any, finish any) {
		raw, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	if n == 1 {
		send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "load_tools", "arguments": `{"names":["git`}}}}, nil)
		send(map[string]any{}, "length")
	} else {
		send(map[string]any{"content": "done"}, nil)
		send(map[string]any{}, "stop")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// No call of a response the output limit cut off runs, and the model is told why.
func TestACutOffResponseRunsNoCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(&cutOffModel{})
	defer srv.Close()
	ended := make(chan db.TurnRow, 1)
	ag, store := started(ctx, t, orchestrator.Config{OnEvent: func(e orchestrator.Event) {
		if e.Kind == orchestrator.EventTurnCompleted {
			ended <- e.Turn
		}
	}}, srv.URL)
	thread, err := ag.StartThread(db.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if turn := endOf(ctx, t, ended); turn.Status != db.TurnCompleted {
		t.Fatalf("the turn: %s %q", turn.Status, turn.Error)
	}
	view, err := store.Thread(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range view.Items {
		if it.IsCall() && (it.Status != db.ItemFailed || !strings.Contains(it.Error, "output limit")) {
			t.Errorf("the cut-off call: %s %q", it.Status, it.Error)
		}
	}
}

// slowMove holds a folder's move until released, as a cloud sandbox's mv takes its time.
type slowMove struct {
	sandbox.Box
	moving  chan struct{}
	release chan struct{}
}

func (b slowMove) Move(ctx context.Context, from, to string) error {
	close(b.moving)
	<-b.release
	return b.Box.Move(ctx, from, to)
}

// While a workspace's folder moves, none of its conversations starts a turn in it.
func TestNoTurnStartsWhileTheWorkspaceMoves(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(&recordingModel{})
	defer srv.Close()
	box := slowMove{Box: sandbox.Here(), moving: make(chan struct{}), release: make(chan struct{})}
	ag, _ := started(ctx, t, orchestrator.Config{BoxFor: func(string) (sandbox.Box, error) { return box, nil }}, srv.URL)
	from, to := t.TempDir(), t.TempDir()
	for _, dir := range []*string{&from, &to} {
		resolved, err := sandbox.CanonicalDirectory(*dir)
		if err != nil {
			t.Fatal(err)
		}
		*dir = resolved
	}
	if err := os.WriteFile(from+"/notes.txt", []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := ag.AddWorkspace(ctx, db.DefaultUser, from, "app")
	if err != nil {
		t.Fatal(err)
	}
	thread, err := ag.StartThread(db.DefaultUser, w.ID, "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	moved := make(chan error, 1)
	go func() {
		_, err := ag.MoveWorkspace(ctx, db.DefaultUser, w.ID, to)
		moved <- err
	}()
	<-box.moving
	if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: "hello"}); !errors.Is(err, orchestrator.ErrWorkspaceBusy) {
		t.Errorf("a turn during the move: %v", err)
	}
	close(box.release)
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
}

// A conversation takes only so many turns waiting behind the one in progress.
func TestAConversationQueuesOnlySoManyTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &holdingModel{release: make(chan struct{})}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	defer close(fake.release)
	ag, _ := started(ctx, t, orchestrator.Config{}, srv.URL)
	thread, err := ag.StartThread(db.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: fmt.Sprintf("message %d", i)}); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := ag.StartTurn(ctx, db.DefaultUser, thread.ID, orchestrator.Input{Text: "one more"}); !errors.Is(err, orchestrator.ErrQueueFull) {
		t.Errorf("past the queue: %v", err)
	}
}

func TestSkillsForNoRoleKeepTheOrchestratorFromStarting(t *testing.T) {
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "main-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	ag := orchestrator.New(orchestrator.Config{
		Name: "Ollupi", DB: store, SkillsDir: dir,
		Models:      []llm.Model{{Name: "demo", Connection: "fake"}},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI("http://127.0.0.1:1", "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
	})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := ag.Start(ctx); err == nil || !strings.Contains(err.Error(), `"main-agent", which is no role`) {
		t.Errorf("skills for no role: %v", err)
	}
}

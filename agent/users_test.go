package agent_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

// Everything that names a thread or workspace treats another user's as though it did not exist.
func TestAUserCannotReachAnotherUsersThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(&recordingModel{})
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	ag := agent.New(agent.Config{
		Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
		DB:          db,
	})
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()

	root := t.TempDir()
	w, err := ag.AddWorkspace("a", root, "app")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := ag.AddWorkspace("b", root, "app")
	if err != nil {
		t.Fatalf("the same path for a second user: %v", err)
	}
	if theirs.ID == w.ID {
		t.Fatal("two users share one workspace")
	}
	thread, err := ag.StartThread("a", w.ID, "mine", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	for name, try := range map[string]func() error{
		"start a thread in the workspace": func() error {
			_, err := ag.StartThread("b", w.ID, "x", permission.Settings{})
			return err
		},
		"rename the workspace":  func() error { _, err := ag.RenameWorkspace("b", w.ID, "x"); return err },
		"archive the workspace": func() error { _, err := ag.ArchiveWorkspace("b", w.ID, true); return err },
		"change the workspace's settings": func() error {
			_, err := ag.UpdateWorkspaceSettings("b", w.ID, permission.Settings{})
			return err
		},
		"change the thread's settings": func() error {
			_, err := ag.UpdateThreadSettings("b", thread.ID, permission.Settings{})
			return err
		},
		"rename the thread":  func() error { _, err := ag.RenameThread("b", thread.ID, "x"); return err },
		"archive the thread": func() error { _, err := ag.ArchiveThread("b", thread.ID, true); return err },
		"delete the thread":  func() error { return ag.DeleteThread("b", thread.ID) },
		"start a turn": func() error {
			_, err := ag.StartTurn("b", thread.ID, agent.Input{Text: "hello"})
			return err
		},
		"steer": func() error {
			_, err := ag.SteerTurn("b", thread.ID, "turn", agent.Input{Text: "hello"})
			return err
		},
		"interrupt":   func() error { return ag.InterruptTurn("b", thread.ID, "") },
		"approve":     func() error { return ag.ResolveApproval("b", thread.ID, "approval", repo.DecisionAccept, "b") },
		"answer":      func() error { return ag.AnswerUserInput("b", thread.ID, "item", userinput.Answers{}) },
		"read a file": func() error { _, _, err := ag.ReadAttachment("b", thread.ID, "file"); return err },
	} {
		if err := try(); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("b tried to %s: %v", name, err)
		}
	}
	if got := ag.PendingRequests("b", thread.ID); got != nil {
		t.Errorf("b sees requests: %+v", got)
	}
	if rows, _, err := ag.ListThreads("b", repo.ThreadFilter{Limit: 10}); err != nil || len(rows) != 0 {
		t.Errorf("b lists threads: %+v, %v", rows, err)
	}
	if rows, _, err := ag.ListThreads("a", repo.ThreadFilter{Limit: 10}); err != nil || len(rows) != 1 || rows[0].Title != "mine" {
		t.Errorf("a lists threads: %+v, %v", rows, err)
	}
	if err := ag.StartScheduledTurn(thread.ID, "scheduled", "job"); err != nil {
		t.Errorf("a scheduled task in a's thread: %v", err)
	}
}

// holdingModel keeps every request open until released, and remembers the most it held at once.
type holdingModel struct {
	mu      sync.Mutex
	held    int
	most    int
	release chan struct{}
}

func (m *holdingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.held++
	m.most = max(m.most, m.held)
	m.mu.Unlock()
	select {
	case <-m.release:
	case <-r.Context().Done():
	}
	m.mu.Lock()
	m.held--
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"demo","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (m *holdingModel) holding() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held
}

// A user at their limit waits while another user's turn takes a free slot, though theirs came first.
func TestAUserHoldsNoMoreSlotsThanTheirLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &holdingModel{release: make(chan struct{})}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	var ended atomic.Int32
	ag := agent.New(agent.Config{
		Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 16, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
		Slots:       8, UserSlots: 2, DB: db,
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventTurnCompleted {
				ended.Add(1)
			}
		},
	})
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()

	start := func(user string) {
		t.Helper()
		thread, err := ag.StartThread(user, "", "t", permission.Settings{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ag.StartTurn(user, thread.ID, agent.Input{Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			if ctx.Err() != nil {
				t.Fatalf("waiting for %s: %d model requests held", what, fake.holding())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for range 5 {
		start("a")
	}
	waitFor("a to reach their limit", func() bool { return fake.holding() == 2 })
	start("b")
	waitFor("b to get a slot", func() bool { return fake.holding() == 3 })
	close(fake.release)
	waitFor("every turn to end", func() bool { return ended.Load() == 6 })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.most != 3 {
		t.Errorf("at most %d requests at once, want a's 2 and b's 1", fake.most)
	}
}

// Each user's calls go through the client given for them, and a user with none fails only their own turn.
func TestTurnsRunOnTheUsersClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	shared, own := &holdingModel{release: make(chan struct{})}, &holdingModel{release: make(chan struct{})}
	close(shared.release)
	close(own.release)
	sharedSrv, ownSrv := httptest.NewServer(shared), httptest.NewServer(own)
	defer sharedSrv.Close()
	defer ownSrv.Close()
	limits := map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}}
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	ownClient := llm.NewOpenAI(ownSrv.URL, "", 4, limits)
	ended := make(chan repo.TurnRow, 2)
	ag := agent.New(agent.Config{
		Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(sharedSrv.URL, "", 4, limits)},
		ClientFor: func(user, connection string) (llm.Client, error) {
			if user == "a" && connection == "fake" {
				return ownClient, nil
			}
			return nil, errors.New("no key for this user")
		},
		DB: db,
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventTurnCompleted {
				ended <- e.Turn
			}
		},
	})
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()
	for _, user := range []string{"a", "b"} {
		thread, err := ag.StartThread(user, "", "t", permission.Settings{})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := ag.StartTurn(user, thread.ID, agent.Input{Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-ended:
			want := repo.TurnCompleted
			if user == "b" {
				want = repo.TurnFailed
			}
			if got.ID != turn.ID || got.Status != want {
				t.Errorf("%s's turn: %s %q", user, got.Status, got.Error)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	own.mu.Lock()
	defer own.mu.Unlock()
	if own.most != 1 || shared.most != 0 {
		t.Errorf("own client took %d requests, shared %d", own.most, shared.most)
	}
}

// A second process waits for the shard, takes it when the first stops, and a process whose shard is taken stops.
func TestAStandbyTakesTheShard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	srv := httptest.NewServer(&recordingModel{})
	defer srv.Close()
	home := t.TempDir()
	open := func() *repo.DB {
		db, err := repo.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	process := func(db *repo.DB, lost func()) *agent.Agent {
		return agent.New(agent.Config{
			Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
			Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
			Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
			DB:          db, Lease: 300 * time.Millisecond, OnLeaseLost: lost,
		})
	}
	first := process(open(), nil)
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	if err := first.Start(firstCtx); err != nil {
		t.Fatal(err)
	}

	lost := make(chan struct{})
	second := process(open(), func() { close(lost) })
	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()
	started := make(chan error, 1)
	go func() { started <- second.Start(secondCtx) }()
	select {
	case err := <-started:
		t.Fatalf("started while the shard was held: %v", err)
	case <-time.After(time.Second):
	}
	stopFirst()
	first.Wait()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the standby never took the shard")
	}

	thief := open()
	if held, err := thief.ClaimShard("thief", time.Now().Add(time.Hour), time.Hour); err != nil || !held {
		t.Fatal(held, err)
	}
	select {
	case <-lost:
	case <-ctx.Done():
		t.Fatal("the process never saw it lost the shard")
	}
	second.Wait()
}

// stubMachine is a sandbox with one folder, which this machine does not have.
type stubMachine struct{ sandbox.Machine }

func (stubMachine) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	if path != "/cloud/app" {
		return nil, fs.ErrNotExist
	}
	return fstest.MapFS{"app": {Mode: fs.ModeDir}}.Stat("app")
}

func TestAWorkspaceIsFoundInTheUsersSandbox(t *testing.T) {
	srv := httptest.NewServer(&recordingModel{})
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ag := agent.New(agent.Config{
		DB: db,
		BoxFor: func(string) (sandbox.Box, error) {
			return sandbox.Remote(stubMachine{}, []string{"/cloud"}, "cloud"), nil
		},
	})
	w, err := ag.AddWorkspace(repo.DefaultUser, "/cloud/app/", "app")
	if err != nil {
		t.Fatal(err)
	}
	if w.RootPath != "/cloud/app" {
		t.Errorf("root %q", w.RootPath)
	}
	again, err := ag.AddWorkspace(repo.DefaultUser, "/cloud/app", "app")
	if err != nil || again.ID != w.ID {
		t.Errorf("the same folder again: %+v, %v", again, err)
	}
	if _, err := ag.AddWorkspace(repo.DefaultUser, "/cloud/missing", "x"); err == nil {
		t.Error("a folder the sandbox does not have was added")
	}
}

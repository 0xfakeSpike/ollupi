package agent_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo"
)

func started(ctx context.Context, t *testing.T, cfg agent.Config, url string) (*agent.Agent, *repo.DB) {
	t.Helper()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg.Name, cfg.DB = "Ollupi", db
	cfg.Models = []llm.Model{{Name: "demo", Connection: "fake"}}
	cfg.Settings = permission.Settings{ApprovalPolicy: string(permission.Never)}
	cfg.Connections = map[string]llm.Client{"fake": llm.NewOpenAI(url, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})}
	ag := agent.New(cfg)
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(ag.Wait)
	t.Cleanup(stop)
	return ag, db
}

func endOf(ctx context.Context, t *testing.T, ended <-chan repo.TurnRow) repo.TurnRow {
	t.Helper()
	select {
	case turn := <-ended:
		return turn
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return repo.TurnRow{}
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
	ag, _ := started(ctx, t, agent.Config{
		Attachments: &memoryStore{objects: map[string][]byte{}},
		BoxFor: func(string) (sandbox.Box, error) {
			return sandbox.Remote(machine, []string{"/cloud"}, "cloud"), nil
		},
	}, srv.URL)
	stuck, err := ag.StartThread(repo.DefaultUser, "", "", "stuck", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ag.StartThread(repo.DefaultUser, "", "", "other", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := ag.StartTurn(repo.DefaultUser, stuck.ID, agent.Input{Text: "read", Attachments: []agent.AttachmentInput{{Path: "/cloud/note.txt"}}})
		sent <- err
	}()
	<-machine.asked

	renamed := make(chan error, 1)
	go func() {
		_, err := ag.RenameThread(repo.DefaultUser, other.ID, "renamed")
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
	ended := make(chan repo.TurnRow, 1)
	ag, _ := started(ctx, t, agent.Config{
		ClientFor: func(string, string) (llm.Client, error) { panic("the secret detail") },
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventTurnCompleted {
				ended <- e.Turn
			}
		},
	}, srv.URL)
	thread, err := ag.StartThread(repo.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.StartTurn(repo.DefaultUser, thread.ID, agent.Input{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	turn := endOf(ctx, t, ended)
	if turn.Status != repo.TurnFailed || strings.Contains(turn.Error, "secret") {
		t.Errorf("the turn: %s %q", turn.Status, turn.Error)
	}
	if _, err := ag.RenameThread(repo.DefaultUser, thread.ID, "still here"); err != nil {
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
	ended := make(chan repo.TurnRow, 1)
	ag, _ := started(ctx, t, agent.Config{
		RunLimit: 200 * time.Millisecond,
		OnEvent: func(e agent.Event) {
			if e.Kind == agent.EventTurnCompleted {
				ended <- e.Turn
			}
		},
	}, srv.URL)
	thread, err := ag.StartThread(repo.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.StartTurn(repo.DefaultUser, thread.ID, agent.Input{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if turn := endOf(ctx, t, ended); turn.Status != repo.TurnFailed || !strings.Contains(turn.Error, "time limit") {
		t.Errorf("the turn: %s %q", turn.Status, turn.Error)
	}
}

func TestPendingRequestsReportStorageThatFails(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ag := agent.New(agent.Config{DB: db})
	if got, err := ag.AllPendingRequests(repo.DefaultUser); err == nil {
		t.Errorf("all requests: %+v", got)
	}
	if got, err := ag.PendingRequests(repo.DefaultUser, "t"); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Errorf("a thread's requests: %+v, %v", got, err)
	}
}

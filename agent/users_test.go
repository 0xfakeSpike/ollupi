package agent_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
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

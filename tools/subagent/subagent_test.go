package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestACallQueuesTheSubagentsConversationAndWaitsOnIt(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	model := llm.Model{Name: "m", Connection: "c"}
	if err := db.CreateThread(repo.ThreadRow{ID: "main", UserID: repo.DefaultUser, Title: "main", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.SubmitTurn("main", repo.TurnRow{ID: "asked", Model: model, Agent: "full-main-agent", Input: "go", CreatedAt: at, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	launch := New(db, []Kind{{Tool: "BrowserAgent", Title: "Browser subagent", Description: "Browse.", Role: "browser-sub-agent"}})
	ctx, caller := context.Background(), tools.Caller{UserID: repo.DefaultUser, ThreadID: "main", Item: "call"}
	args := map[string]any{"description": "Look it up", "prompt": "Find the page and report."}

	res, err := launch.Invoke(ctx, caller, "BrowserAgent", args)
	if err != nil || res.Await == nil || res.Await.By != tools.AwaitAgent || res.Await.Handle == "" {
		t.Fatalf("a launch: %+v %v", res, err)
	}
	child, err := db.ThreadState(res.Await.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if child.Thread.ParentThreadID != "main" || child.Thread.ParentItemID != "call" || child.Thread.Title != "Look it up" {
		t.Errorf("the subagent's conversation: %+v", child.Thread)
	}
	queued := child.QueuedTurns()
	if _, active := child.ActiveTurn(); active || len(queued) != 1 || queued[0].Agent != "browser-sub-agent" ||
		queued[0].Input != "Find the page and report." || queued[0].Model != model {
		t.Errorf("the subagent's turn: %+v", queued)
	}

	again, err := launch.Invoke(ctx, caller, "BrowserAgent", args)
	if err != nil || again.Await == nil || again.Await.Handle != res.Await.Handle {
		t.Errorf("the same call launched another subagent: %+v %v", again, err)
	}
	if _, err := launch.Invoke(ctx, caller, "BrowserAgent", map[string]any{"description": "x", "prompt": "  "}); err == nil {
		t.Error("a blank task launched a subagent")
	}
	if _, err := launch.Invoke(ctx, tools.Caller{UserID: repo.DefaultUser, ThreadID: "main"}, "BrowserAgent", args); err == nil {
		t.Error("a launch outside a call of a turn queued a subagent")
	}
}

package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestACallWritesTheSubagentsConversationAndWaitsOnIt(t *testing.T) {
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	model := llm.Model{Name: "m", Connection: "c"}
	if err := store.CreateThread(db.ThreadRow{ID: "main", UserID: db.DefaultUser, Title: "main", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := store.SubmitTurn("main", db.TurnRow{ID: "asked", Model: model, Agent: "full-main-agent", Input: "go", CreatedAt: at, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	launch := New(store)
	ctx, caller := context.Background(), tools.Caller{UserID: db.DefaultUser, ThreadID: "main", Item: "call"}
	args := map[string]any{"description": "Look it up", "prompt": "Find the page and report."}

	res, err := launch.Invoke(ctx, caller, Browser, args)
	if err != nil || res.Await == nil || res.Await.By != tools.AwaitAgent || res.Await.Handle == "" {
		t.Fatalf("a launch: %+v %v", res, err)
	}
	child, err := store.ThreadState(res.Await.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if child.Thread.ParentThreadID != "main" || child.Thread.ParentItemID != "call" || child.Thread.Title != "Look it up" {
		t.Errorf("the subagent's conversation: %+v", child.Thread)
	}
	turn, active := child.ActiveTurn()
	if !active || len(child.Open) != 1 || turn.Status != db.TurnReady || turn.Agent != Browser || turn.Model != model {
		t.Errorf("the subagent's turn: %+v", child.Open)
	}
	if items, err := store.TurnItems(turn.ID); err != nil || len(items) != 1 || items[0].Type != db.ItemUserMessage || items[0].Text != "Find the page and report." {
		t.Errorf("the subagent's task: %+v (%v)", items, err)
	}

	again, err := launch.Invoke(ctx, caller, Browser, args)
	if err != nil || again.Await == nil || again.Await.Handle != res.Await.Handle {
		t.Errorf("the same call launched another subagent: %+v %v", again, err)
	}
	if _, err := launch.Invoke(ctx, caller, Browser, map[string]any{"description": "x", "prompt": "  "}); err == nil {
		t.Error("a blank task launched a subagent")
	}
	if _, err := launch.Invoke(ctx, tools.Caller{UserID: db.DefaultUser, ThreadID: "main"}, Browser, args); err == nil {
		t.Error("a launch outside a call of a turn wrote a subagent")
	}
}

package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/user"
)

func TestATaskMadeInAConversationIsListedPausedAndDeleted(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	err = st.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		return tx.CreateThread(store.Thread{User: "u", Thread: user.Thread{ID: "th", Title: "news", Created: now, Updated: now}})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := New(st, cron.NewCalendar(time.UTC))
	call := func(name string, args map[string]any) (any, error) {
		r, err := p.Invoke(ctx, tool.Call{Caller: tool.Caller{User: "u", Thread: "th"}, Tool: name, Arguments: args})
		return r.Output, err
	}
	if _, err := call(CreateTool, map[string]any{"name": "daily", "spec": "@daily", "prompt": "read the news"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(CreateTool, map[string]any{"name": "daily", "spec": "@hourly", "prompt": "again"}); err == nil {
		t.Error("a second task of the same name")
	}
	if out, err := call(PauseTool, map[string]any{"name": "daily", "paused": true}); err != nil || !out.(job).Paused {
		t.Errorf("paused %+v, %v", out, err)
	}
	if out, _ := call(ListTool, map[string]any{}); len(out.(map[string]any)["jobs"].([]job)) != 1 {
		t.Errorf("listed %+v", out)
	}
	if _, err := call(DeleteTool, map[string]any{"name": "daily"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(ListTool, map[string]any{}); len(out.(map[string]any)["jobs"].([]job)) != 0 {
		t.Errorf("left %+v", out)
	}
}

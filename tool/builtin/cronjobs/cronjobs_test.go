package cronjobs

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
)

func TestACronJobMadeInAConversationIsListedPausedAndDeleted(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	err = st.Update(ctx, func(tx store.Tx) error {
		if err := tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		scratch, err := scratchOf(tx, "u")
		if err != nil {
			return err
		}
		return tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: "th", Workspace: scratch, Title: "news", Created: now, Updated: now}})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := New(st, cron.NewCalendar(time.UTC))
	call := func(name string, args map[string]any) (any, error) {
		r, err := p.Invoke(ctx, tool.Call{Caller: tool.Caller{User: "u", Thread: "th"}, Tool: name, Arguments: args})
		return r.Output, err
	}
	if _, err := call(createTool, map[string]any{"name": "daily", "spec": "@daily", "prompt": "read the news"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(createTool, map[string]any{"name": "daily", "spec": "@hourly", "prompt": "again"}); err == nil {
		t.Error("a second task of the same name")
	}
	if out, err := call(pauseTool, map[string]any{"name": "daily", "paused": true}); err != nil || !out.(listedJob).Paused {
		t.Errorf("paused %+v, %v", out, err)
	}
	if out, _ := call(listTool, map[string]any{}); len(out.(map[string]any)["jobs"].([]listedJob)) != 1 {
		t.Errorf("listed %+v", out)
	}
	if _, err := call(deleteTool, map[string]any{"name": "daily"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(listTool, map[string]any{}); len(out.(map[string]any)["jobs"].([]listedJob)) != 0 {
		t.Errorf("left %+v", out)
	}
}

// scratchOf is the scratch workspace of the user's first environment, where
// a conversation started in no workspace is.
func scratchOf(r store.Reader, user string) (string, error) {
	envs, err := r.Environments(user)
	if err != nil {
		return "", err
	}
	w, err := r.Scratch(user, envs[0].ID)
	return w.ID, err
}

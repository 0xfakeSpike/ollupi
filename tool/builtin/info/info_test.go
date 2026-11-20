package info

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
)

func TestTheAgentLearnsItsConversationAndTheUsersWorkspaces(t *testing.T) {
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	err = st.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.EnsureUser("u"); err != nil {
			return err
		}
		for _, w := range []account.Workspace{
			{ID: "w1", Name: "docs", Root: "/docs", Created: now, Updated: now},
			{ID: "w2", Name: "home", Root: "/home", Created: now, Updated: now},
			{ID: "w3", Name: "old", Root: "/old", Created: now, Updated: now, Archived: &now},
		} {
			if err := tx.PutWorkspace("u", w); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(st).Invoke(context.Background(), tool.Call{Caller: tool.Caller{User: "u", Thread: "th", Workspace: "w2"}})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Output.(Info)
	if got.Conversation != "th" || got.Workspace != "w2" || len(got.Workspaces) != 2 || got.Workspaces[0] != (Workspace{ID: "w1", Name: "docs", Root: "/docs"}) {
		t.Errorf("the info %+v", got)
	}
}

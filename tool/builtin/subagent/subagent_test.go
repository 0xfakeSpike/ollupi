package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
)

func TestALaunchNamesItsRoleAndWorkspace(t *testing.T) {
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
		if err := tx.PutWorkspace("u", account.Workspace{ID: "docs", Name: "docs", Root: "/docs", Created: now, Updated: now}); err != nil {
			return err
		}
		return tx.PutWorkspace("u", account.Workspace{ID: "old", Name: "old", Root: "/old", Created: now, Updated: now, Archived: &now})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := New(st)
	launch := func(role agent.Name, args map[string]any) (*tool.Launch, error) {
		args["description"], args["prompt"] = "write", "write the page"
		res, err := p.Invoke(context.Background(), tool.Call{Caller: tool.Caller{User: "u", Workspace: "home"}, Tool: string(role), Arguments: args})
		return res.Launch, err
	}
	if l, err := launch(agent.GeneralSub, map[string]any{}); err != nil || l.Workspace != "home" || l.Role != string(agent.GeneralSub) {
		t.Errorf("by default: %+v, %v", l, err)
	}
	if l, err := launch(agent.DelegateSub, map[string]any{"workspace": "docs"}); err != nil || l.Workspace != "docs" || l.Role != string(agent.DelegateSub) {
		t.Errorf("a delegate in docs: %+v, %v", l, err)
	}
	if _, err := launch(agent.DelegateSub, map[string]any{"workspace": "nowhere"}); err == nil || !strings.Contains(err.Error(), agent.InfoTool) {
		t.Errorf("an unknown workspace: %v", err)
	}
	if _, err := launch(agent.GeneralSub, map[string]any{"workspace": "old"}); !errors.Is(err, account.ErrWorkspaceArchived) {
		t.Errorf("an archived workspace: %v", err)
	}
}

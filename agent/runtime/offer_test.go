package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/role"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/tools"
)

type pageTools struct{}

func (pageTools) Name() (string, string) { return role.BrowserProvider, "Drives a browser tab." }

func (pageTools) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{Name: "navigate", Description: "Opens a page.", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (pageTools) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{}, nil
}

func offered(t *testing.T, providers ...tools.Provider) []string {
	t.Helper()
	registry, err := tools.New(context.Background(), nil, append(providers, role.Launchers{})...)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := permission.New([]permission.Layer{{Of: "test", Settings: permission.Settings{ApprovalPolicy: string(permission.OnRequest)}}}, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := role.Of(role.FullMain)
	rt := &Runtime{Role: full, Window: contextmanager.New("m", llm.Limits{Context: 20000, Output: 2000})}
	rt.offer(checker)
	var names []string
	for _, d := range rt.offered {
		names = append(names, d.Name)
	}
	return names
}

func TestTheBrowserSubagentIsHiddenWithoutABrowser(t *testing.T) {
	if got := offered(t); slices.Contains(got, role.BrowserSubTool) || !slices.Contains(got, role.GeneralSubTool) {
		t.Errorf("without a browser: offered %q", got)
	}
	if got := offered(t, pageTools{}); !slices.Contains(got, role.BrowserSubTool) {
		t.Errorf("with a browser: offered %q", got)
	}
}

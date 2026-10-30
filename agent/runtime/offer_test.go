package runtime

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func tool(name, description string) tools.ToolDescriptor {
	provider, _, _ := strings.Cut(name, tools.ProviderSeparator)
	return tools.ToolDescriptor{Provider: provider, Name: name, Description: description}
}

func names(ds []tools.ToolDescriptor) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func loadCall(status repo.ItemStatus, loaded ...string) repo.ItemRow {
	out, _ := json.Marshal(map[string]any{"loaded": loaded})
	return repo.ItemRow{Type: repo.ItemToolCall, Tool: tools.ToolLoadTools, Status: status, Result: &repo.ToolResult{Output: out}}
}

var window = contextmanager.New("m", llm.Limits{Context: 10000, Output: 1000})

func TestOfferSendsEveryToolWithinTheBudget(t *testing.T) {
	offered, deferred := Offer([]tools.ToolDescriptor{tool("shell", "run"), tool(tools.ToolLoadTools, "load"), tool("github__issue", "file")}, window, nil)
	if got, want := names(offered), []string{"shell", "github__issue"}; !slices.Equal(got, want) || deferred != nil {
		t.Errorf("offered %q, deferred %q; want %q and nothing deferred", got, names(deferred), want)
	}
}

func TestOfferDefersExternalToolsOverTheBudget(t *testing.T) {
	long := strings.Repeat("word ", 1000)
	visible := []tools.ToolDescriptor{tool("shell", "run"), tool(tools.ToolLoadTools, "load"), tool("github__issue", long), tool("github__pr", long)}
	offered, deferred := Offer(visible, window, nil)
	if got, want := names(offered), []string{"shell", tools.ToolLoadTools}; !slices.Equal(got, want) {
		t.Errorf("offered %q, want %q", got, want)
	}
	if got, want := names(deferred), []string{"github__issue", "github__pr"}; !slices.Equal(got, want) {
		t.Errorf("deferred %q, want %q", got, want)
	}

	items := []repo.ItemRow{loadCall(repo.ItemCompleted, "github__pr", "shell"), loadCall(repo.ItemFailed, "github__issue"), loadCall(repo.ItemCompleted, "github__pr")}
	offered, deferred = Offer(visible, window, items)
	if got, want := names(offered), []string{"shell", tools.ToolLoadTools, "github__pr"}; !slices.Equal(got, want) || len(deferred) != 2 {
		t.Errorf("after loading: offered %q, deferred %q", got, names(deferred))
	}

	without := slices.DeleteFunc(slices.Clone(visible), func(d tools.ToolDescriptor) bool { return d.Name == tools.ToolLoadTools })
	if offered, deferred := Offer(without, window, nil); len(offered) != 3 || deferred != nil {
		t.Errorf("without load_tools: offered %q, deferred %q; want everything offered", names(offered), names(deferred))
	}
}

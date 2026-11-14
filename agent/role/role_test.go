package role

import (
	"context"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

func TestEveryNameIsARole(t *testing.T) {
	if len(NameValues) != len(all) {
		t.Fatalf("%d names for %d roles", len(NameValues), len(all))
	}
	for _, name := range NameValues {
		if r, ok := Of(name); !ok || r.Name != name {
			t.Errorf("%s names no role", name)
		}
	}
}

func TestASmallInputGetsTheCompactMainAgent(t *testing.T) {
	if r := ForMain(compactBelow - 1); r.Name != CompactMain {
		t.Errorf("a small input: %s", r.Name)
	}
	if r := ForMain(compactBelow); r.Name != FullMain {
		t.Errorf("an input that holds the full prompt: %s", r.Name)
	}
}

func TestEachRolesPermissionKeepsItsTools(t *testing.T) {
	shell := tools.ToolDescriptor{Provider: "workspace", Name: "run_command"}
	page := tools.ToolDescriptor{Provider: BrowserProvider, Name: "navigate"}
	job := tools.ToolDescriptor{Provider: cron.ProviderName, Name: cron.ToolCreateJob}
	output := tools.ToolDescriptor{Provider: contextmanager.OutputsProvider, Name: contextmanager.ToolReadOutput}
	launchers := []tools.ToolDescriptor{{Provider: LaunchersProvider, Name: GeneralSubTool}, {Provider: LaunchersProvider, Name: BrowserSubTool}}
	for _, r := range all {
		main := r.Launcher == nil
		for _, l := range launchers {
			if r.Permission.Keeps(l) != main {
				t.Errorf("%s keeps %s: %v", r.Name, l.Name, r.Permission.Keeps(l))
			}
		}
		if r.Permission.Keeps(job) != main {
			t.Errorf("%s keeps a scheduled-task tool: %v", r.Name, r.Permission.Keeps(job))
		}
		if !r.Permission.Keeps(output) {
			t.Errorf("%s cannot read a shortened output", r.Name)
		}
	}
	general, _ := Of(GeneralSub)
	browser, _ := Of(BrowserSub)
	if !general.Permission.Keeps(shell) || !general.Permission.Keeps(page) {
		t.Error("the general subagent lacks a tool of its parent")
	}
	if browser.Permission.Keeps(shell) || !browser.Permission.Keeps(page) {
		t.Error("the browser subagent keeps other than the browser's tools")
	}
}

func TestLaunchersLaunchEverySubagentRole(t *testing.T) {
	specs, err := Launchers{}.ToolList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		r, ok := Launched(s.Name)
		if !ok || !strings.HasPrefix(s.Description, r.Launcher.Description) {
			t.Errorf("%s launches no role", s.Name)
		}
		names = append(names, string(r.Name))
	}
	if strings.Join(names, " ") != string(GeneralSub)+" "+string(BrowserSub) {
		t.Errorf("the launchers launch %q", names)
	}
}

func TestASubagentNeedsATaskToDo(t *testing.T) {
	res, err := Launchers{}.Invoke(context.Background(), GeneralSubTool, map[string]any{"description": "Read the docs", "prompt": "Summarize docs/"})
	if err != nil || res.Await == nil || res.Await.By != tools.AwaitAgent {
		t.Fatalf("a task: %+v %v", res, err)
	}
	call, err := Decode(res.Await.Request)
	if err != nil || call.Prompt != "Summarize docs/" {
		t.Errorf("the call it waits on: %+v %v", call, err)
	}
	if _, err := (Launchers{}).Invoke(context.Background(), GeneralSubTool, map[string]any{"description": "x", "prompt": "  "}); err == nil {
		t.Error("a blank prompt launched a subagent")
	}
}

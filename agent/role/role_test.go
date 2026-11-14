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

func TestEachRoleIsOfferedItsTools(t *testing.T) {
	shell := tools.ToolDescriptor{Name: "run_command"}
	page := tools.ToolDescriptor{Provider: BrowserProvider, Name: "navigate"}
	job := tools.ToolDescriptor{Provider: cron.ProviderName, Name: cron.ToolCreateJob}
	launchers := []tools.ToolDescriptor{{Name: GeneralSubTool}, {Name: BrowserSubTool}}
	for _, r := range all {
		main := r.Launcher == nil
		for _, l := range launchers {
			if r.Offers(l) != main {
				t.Errorf("%s offered %s: %v", r.Name, l.Name, r.Offers(l))
			}
		}
		if r.Offers(job) != main {
			t.Errorf("%s offered a scheduled-task tool: %v", r.Name, r.Offers(job))
		}
	}
	general, _ := Of(GeneralSub)
	browser, _ := Of(BrowserSub)
	if !general.Offers(shell) || !general.Offers(page) {
		t.Error("the general subagent lacks a tool of its parent")
	}
	if browser.Offers(shell) || !browser.Offers(page) {
		t.Error("the browser subagent is offered other than the browser's tools")
	}
	for _, r := range all {
		if !r.Offers(tools.ToolDescriptor{Name: contextmanager.ToolReadOutput}) {
			t.Errorf("%s cannot read a shortened output", r.Name)
		}
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

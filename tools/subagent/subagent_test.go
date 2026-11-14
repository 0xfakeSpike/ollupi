package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

func TestASubagentNeedsATaskToDo(t *testing.T) {
	res, err := New().Invoke(context.Background(), Tool, map[string]any{"description": "Read the docs", "prompt": "Summarize docs/"})
	if err != nil || res.Await == nil || res.Await.By != tools.AwaitAgent {
		t.Fatalf("a task: %+v %v", res, err)
	}
	call, err := Decode(res.Await.Request)
	if err != nil || call.Prompt != "Summarize docs/" {
		t.Errorf("the call it waits on: %+v %v", call, err)
	}
	if _, err := New().Invoke(context.Background(), Tool, map[string]any{"description": "x", "prompt": "  "}); err == nil {
		t.Error("a blank prompt launched a subagent")
	}
}

func TestEachSubagentIsOfferedItsTools(t *testing.T) {
	general, _ := KindOf(Tool)
	browser, _ := KindOf(BrowserTool)
	shell := tools.ToolDescriptor{Name: "run_command"}
	page := tools.ToolDescriptor{Provider: BrowserProvider, Name: "navigate"}
	job := tools.ToolDescriptor{Provider: cron.ProviderName, Name: cron.ToolCreateJob}
	for _, k := range Kinds {
		for _, other := range Kinds {
			if k.Offers(tools.ToolDescriptor{Name: other.Tool}) {
				t.Errorf("a subagent of %s is offered %s", k.Tool, other.Tool)
			}
		}
		if k.Offers(job) {
			t.Errorf("a subagent of %s is offered a scheduled-task tool", k.Tool)
		}
	}
	if !general.Offers(shell) || !general.Offers(page) {
		t.Error("a general subagent lacks a tool of its parent")
	}
	if browser.Offers(shell) || !browser.Offers(page) {
		t.Error("a browser subagent is offered other than the browser's tools")
	}
}

func TestToolListLaunchesEveryKind(t *testing.T) {
	specs, err := New().ToolList(context.Background())
	if err != nil || len(specs) != len(Kinds) {
		t.Fatalf("%d specs for %d kinds (%v)", len(specs), len(Kinds), err)
	}
	for i, s := range specs {
		if s.Name != Kinds[i].Tool || !strings.HasPrefix(s.Description, Kinds[i].Description) {
			t.Errorf("spec %d: %s", i, s.Name)
		}
	}
}

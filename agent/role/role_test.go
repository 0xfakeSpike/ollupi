package role

import (
	"maps"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
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
	launchTools := []tools.ToolDescriptor{{Provider: subagent.ProviderName, Name: string(GeneralSub)}, {Provider: subagent.ProviderName, Name: string(BrowserSub)}}
	for _, r := range all {
		_, sub := subagent.Kinds[string(r.Name)]
		main := !sub
		for _, l := range launchTools {
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

func TestEachKindOfSubagentRunsAsItsRole(t *testing.T) {
	for name := range subagent.Kinds {
		if r, ok := Of(Name(name)); !ok || r.Name != Name(name) {
			t.Errorf("subagent %s runs as no role", name)
		}
	}
	if len(subagent.Kinds) != 2 {
		t.Errorf("the kinds of subagent are %q", slices.Collect(maps.Keys(subagent.Kinds)))
	}
}

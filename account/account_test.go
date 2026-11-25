package account

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/policy"
)

func TestTheLayersStackFromTheDeploymentToTheConversation(t *testing.T) {
	deployment := policy.Layer{Of: "the default permission settings", Shared: true}
	w := Workspace{Name: "app"}
	layers := Layers(deployment, nil, Settings{Memory: true}, w, Thread{}.Layer())
	var of []string
	for _, l := range layers {
		of = append(of, l.Of)
	}
	want := []string{"the default permission settings", "your permission settings", `the permission settings of workspace "app"`,
		"the providers of this workspace", "this conversation's permission settings"}
	if !slices.Equal(of, want) {
		t.Errorf("layers %q", of)
	}
}

func TestAUserWithoutMemoryIsNotOfferedItsTools(t *testing.T) {
	search := policy.Tool{Name: "memory_search", Provider: MemoryProvider}
	if !(Settings{Memory: true}).Layer().Keeps(search) {
		t.Error("memory's tools are hidden from a user who uses it")
	}
	if (Settings{}).Layer().Keeps(search) {
		t.Error("memory's tools are offered to a user who does not use it")
	}
}

func TestAnMCPServerIsCheckedBeforeItIsKept(t *testing.T) {
	for name, s := range map[string]MCPServer{
		"a name with the separator": {Name: "git__hub", Command: "x"},
		"a provider's name":         {Name: "browser", Command: "x"},
		"both transports":           {Name: "github", Command: "x", URL: "https://x"},
		"neither transport":         {Name: "github"},
		"headers for a command":     {Name: "github", Command: "x", Headers: map[string]string{"a": "b"}},
	} {
		if err := s.Check([]string{"browser"}); !errors.Is(err, ErrInvalidMCPServer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (MCPServer{Name: "github", URL: "https://api.example/mcp", Headers: map[string]string{"Authorization": "x"}}).Check([]string{"browser"}); err != nil {
		t.Error(err)
	}
}

func TestNamesAndKinds(t *testing.T) {
	if _, err := Title("", false); err != nil {
		t.Error("an untitled conversation was refused")
	}
	if _, err := Title(" ", true); !errors.Is(err, ErrInvalidName) {
		t.Error("a rename to no title was taken")
	}
	if name, err := Name("a workspace name", "  app  "); err != nil || name != "app" {
		t.Errorf("%q, %v", name, err)
	}
}

func TestAWorkspaceTurnsProvidersOnAndOff(t *testing.T) {
	optIn := []string{"hub"}
	kept := func(w Workspace, provider string) bool {
		layers := Layers(Deployment(policy.Settings{}), optIn, Settings{}, w, policy.Layer{})
		return policy.Kept(layers, policy.Tool{Name: "x", Provider: provider})
	}
	plain := Workspace{}
	hub := Workspace{Providers: Providers{Enabled: []string{"hub"}, Disabled: []string{"browser"}}}
	for _, c := range []struct {
		w        Workspace
		provider string
		want     bool
	}{
		{plain, "hub", false}, {hub, "hub", true},
		{plain, "browser", true}, {hub, "browser", false}, {hub, "workspace", true},
	} {
		if got := kept(c.w, c.provider); got != c.want {
			t.Errorf("%+v keeps %s: %v", c.w, c.provider, got)
		}
	}
	if _, err := Instructions(strings.Repeat("x", maxInstructionsRunes+1)); !errors.Is(err, ErrInvalidName) {
		t.Errorf("instructions too long: %v", err)
	}
}

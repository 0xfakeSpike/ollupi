package user

import (
	"errors"
	"slices"
	"testing"

	"github.com/0xfakeSpike/ollupi/policy"
)

func TestTheLayersStackFromTheDeploymentToTheConversation(t *testing.T) {
	deployment := policy.Layer{Of: "the default permission settings", Shared: true}
	w := &Workspace{Name: "app"}
	layers := Layers(deployment, Settings{Memory: true}, w, Thread{}.Layer())
	var of []string
	for _, l := range layers {
		of = append(of, l.Of)
	}
	want := []string{"the default permission settings", "your permission settings", `the permission settings of workspace "app"`, "this conversation's permission settings"}
	if !slices.Equal(of, want) {
		t.Errorf("layers %q", of)
	}
	if len(Layers(deployment, Settings{Memory: true}, nil, Thread{}.Layer())) != 3 {
		t.Error("a conversation outside any workspace has a workspace's layer")
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
	if name, err := WorkspaceName("  app  "); err != nil || name != "app" {
		t.Errorf("%q, %v", name, err)
	}
	if kind, err := NewEnvironmentKind(""); err != nil || kind != Machine {
		t.Errorf("%q, %v", kind, err)
	}
	if _, err := NewEnvironmentKind(Local); !errors.Is(err, ErrInvalidName) {
		t.Errorf("a second local environment: %v", err)
	}
}

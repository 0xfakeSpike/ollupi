// Package user is what a user owns and sets for themselves: their settings, their workspaces, the environments their
// conversations run in, their conversations' headers and their MCP servers, with the rules each keeps. It does no I/O.
package user

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/policy"
)

var (
	ErrInvalidName       = errors.New("invalid name")
	ErrWorkspaceArchived = errors.New("the workspace is archived; restore it before starting new work")
	ErrInvalidMCPServer  = errors.New("invalid MCP server")
	ErrSubagentThread    = errors.New("a subagent thread takes no input; send it to its parent conversation")
)

// MemoryProvider is the provider of the tools of long-term memory, which a user who does not use it is not offered.
const MemoryProvider = "memory"

type Settings struct {
	Redaction bool
	// Made once per user, so a value masks to the same placeholder across restarts and what the model is sent stays
	// the same.
	RedactionKey string
	// Off, the user's conversations neither recall nor search long-term memory, and none is made from them.
	Memory      bool
	Permissions policy.Settings
}

// Layer is the user's, between the deployment's and their workspace's.
func (s Settings) Layer() policy.Layer {
	l := policy.Layer{Of: "your permission settings", Settings: s.Permissions}
	if !s.Memory {
		l.Without = []string{MemoryProvider}
	}
	return l
}

type Workspace struct {
	ID       string
	Name     string
	Root     string
	Settings policy.Settings
	Created  time.Time
	Updated  time.Time
	Archived *time.Time
}

func (w Workspace) Layer() policy.Layer {
	return policy.Layer{Of: fmt.Sprintf("the permission settings of workspace %q", w.Name), Settings: w.Settings}
}

func (w Workspace) Usable() error {
	if w.Archived != nil {
		return fmt.Errorf("workspace %s: %w", w.Name, ErrWorkspaceArchived)
	}
	return nil
}

type EnvironmentKind string

const (
	// Local is this machine, which every user has and none removes: their workspaces are folders in it.
	Local   EnvironmentKind = "local"
	Machine EnvironmentKind = "machine"
)

const LocalName = "This computer"

type Environment struct {
	ID      string
	Name    string
	Kind    EnvironmentKind
	Created time.Time
	Updated time.Time
}

// NewEnvironmentKind is the kind of an environment a user adds: a machine, the one they may add.
func NewEnvironmentKind(kind EnvironmentKind) (EnvironmentKind, error) {
	switch kind {
	case "", Machine:
		return Machine, nil
	case Local:
		return "", fmt.Errorf("%w: this machine is one environment and every user has it; name a cloud sandbox instead", ErrInvalidName)
	}
	return "", fmt.Errorf("%w: an environment is %q, not %q", ErrInvalidName, Machine, kind)
}

// Thread is a conversation's header; its log is a conversation.View.
type Thread struct {
	ID          string
	Title       string
	Settings    policy.Settings
	Workspace   string
	Environment string
	// A subagent's conversation has the one that launched it, and the call that did.
	Parent     string
	ParentItem string
	Created    time.Time
	Updated    time.Time
	Archived   *time.Time
	Replied    *time.Time
	Read       *time.Time
}

func (t Thread) Layer() policy.Layer {
	return policy.Layer{Of: "this conversation's permission settings", Settings: t.Settings}
}

// ParentLayer is what a subagent's conversation runs under: the settings of the conversation that launched it.
func (t Thread) ParentLayer() policy.Layer {
	return policy.Layer{Of: "the parent conversation's permission settings", Settings: t.Settings}
}

// Layers stack a call's settings, the later deciding over the earlier: the deployment's, the user's, the workspace's
// when the conversation is in one, the conversation's.
func Layers(deployment policy.Layer, s Settings, w *Workspace, conversation policy.Layer) []policy.Layer {
	out := []policy.Layer{deployment, s.Layer()}
	if w != nil {
		out = append(out, w.Layer())
	}
	return append(out, conversation)
}

const maxNameRunes = 120

func WorkspaceName(name string) (string, error) { return named("a workspace name", name) }

func EnvironmentName(name string) (string, error) { return named("an environment name", name) }

// Title checks a conversation's title; one may start untitled, but not be renamed to none.
func Title(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxNameRunes {
		return "", fmt.Errorf("%w: a title must be 1 to %d characters", ErrInvalidName, maxNameRunes)
	}
	return title, nil
}

func named(what, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxNameRunes {
		return "", fmt.Errorf("%w: %s must be 1 to %d characters", ErrInvalidName, what, maxNameRunes)
	}
	return name, nil
}

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// Check refuses a server that could never start, or whose name is that of a provider the deployment has already.
func (s MCPServer) Check(providers []string) error {
	switch {
	case !serverName.MatchString(s.Name) || strings.Contains(s.Name, policy.ProviderSeparator):
		return fmt.Errorf("%w: the name %q must be 1 to 32 letters, digits, '-' or '_', start with a letter or digit, and not contain %q", ErrInvalidMCPServer, s.Name, policy.ProviderSeparator)
	case slices.Contains(providers, s.Name):
		return fmt.Errorf("%w: %q names tools this agent already has", ErrInvalidMCPServer, s.Name)
	case (s.Command == "") == (s.URL == ""):
		return fmt.Errorf("%w: give either a command (stdio) or a URL (HTTP)", ErrInvalidMCPServer)
	case s.Command == "" && (len(s.Args) > 0 || len(s.Env) > 0):
		return fmt.Errorf("%w: args and env are for a command; this server has a URL", ErrInvalidMCPServer)
	case s.URL == "" && len(s.Headers) > 0:
		return fmt.Errorf("%w: headers are for a URL; this server has a command", ErrInvalidMCPServer)
	}
	return nil
}

// CronJob is a scheduled task: a message the user has sent to a conversation again and again, on a cron schedule. Times
// caps how often, Runs counts how often it has been.
type CronJob struct {
	ID      string
	Thread  string
	Name    string
	Spec    string
	Prompt  string
	Times   *int
	Runs    int
	Paused  bool
	NextRun time.Time
	Created time.Time
}

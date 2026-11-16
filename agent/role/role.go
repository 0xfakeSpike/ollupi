// Package role holds the roles of agent, side by side: the two main agents and the subagents. A turn names the role
// that runs it, and each run, or its recovery after a crash, rebuilds that agent from the role and the turn alone.
// Each role is defined in a file of its own.
package role

import (
	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// Name is a role as a turn stores it.
type Name string

var NameValues = []Name{FullMain, CompactMain, GeneralSub, BrowserSub}

// A Role is one role of agent: what a run of a turn that names it is made of.
type Role interface {
	// Name is the role as a turn stores it.
	Name() Name
	// System writes the role's system prompt for a run: of the deployment's agent, with the skills installed for the
	// role listed within the window's budget for them, in the project at root, for a run that has the tools working in
	// it, and with the deferred tools named for load_tools to load.
	System(root string, deferred []tools.ToolDescriptor) (string, error)
	// Permit makes the role's permissions for a run: what the role itself keeps of the tools, below the deployment's
	// default settings, the workspace's and the conversation's (none outside one), which decide over it, with the
	// tools of registry the deployment allows. The methods below use them.
	Permit(workspace db.WorkspaceRow, registry *tools.Registry, conversation ...permission.Layer) error
	// Decide decides a call as the role's permissions do, with the conversation, whose approvals may allow it.
	Decide(call permission.Call) permission.Decision
	// Offer splits the tools the role's permissions let a run see into the ones sent to the model and the ones only
	// named in DEFERRED TOOLS, for load_tools to load, as the conversation's load_tools calls loaded some of them.
	Offer() (offered, deferred []tools.ToolDescriptor)
	// Tool is the tool of that name the role offers the run, as the conversation is now.
	Tool(name string) (tools.ToolDescriptor, bool)
	// Recall is the memory snapshot the run takes in now, if any: an item for the runtime to record.
	Recall() (*db.ItemRow, error)
	// Window is the context window of the turn's model, which the role fits its prompt, tools and memory to.
	Window() *contextmanager.Manager

	// Redact makes the run's redactor, with the user's key. Mask hides the user's secrets from the model, in what it
	// is sent; Restore, RestoreJSON (in a JSON text) and a Stream (in text that arrives in parts) put them back in what
	// it answers.
	Redact(key string)
	Mask(text string) string
	Restore(text string) string
	RestoreJSON(text string) string
	Stream() *redact.Stream

	// AddUsage adds a model call's usage to the run's, which Usage is.
	AddUsage(u db.Usage)
	Usage() db.Usage

	// Load reads the run's turn from store, its conversation and the conversation's items up to it, and makes the
	// role's permissions there with the tools of registry; Reload reads the turn's items and its conversation again,
	// and makes them anew, as the conversation's settings may have changed.
	Load(store *db.DB, registry *tools.Registry, turnID string) error
	Reload(store *db.DB, registry *tools.Registry) error
	Turn() db.TurnRow
	Thread() db.ThreadRow
	Workspace() db.WorkspaceRow
	// Items are the turn's items, which Conversation ends; Keep takes one stored, new or changed.
	Items() []db.ItemRow
	Conversation() []db.ItemRow
	Keep(stored db.ItemRow)
}

// New makes the role a turn names, as it stores it, for the deployment's agent: called name, which the operator's
// prompt may describe, with each role's skills installed in skillsDir; its default permission settings serve users
// with different tools when shared, and tools, when set, are the only tools a turn may use; memory keeps the users'
// memory, which the roles that take it in recall; window is the context window of the turn's model.
func New(role Name, name, prompt, skillsDir string, settings permission.Settings, shared bool, tools []string, memory *memory.Manager, window *contextmanager.Manager) (Role, bool) {
	switch role {
	case FullMain:
		return NewFullMain(name, prompt, skillsDir, settings, shared, tools, memory, window), true
	case CompactMain:
		return NewCompactMain(name, prompt, skillsDir, settings, shared, tools, memory, window), true
	case GeneralSub:
		return NewGeneralSub(name, prompt, skillsDir, settings, shared, tools, memory, window), true
	case BrowserSub:
		return NewBrowserSub(name, prompt, skillsDir, settings, shared, tools, memory, window), true
	}
	return nil, false
}

// ForMain is the main role for a model that takes inputTokens of input.
func ForMain(inputTokens int) Name {
	if inputTokens < compactBelow {
		return CompactMain
	}
	return FullMain
}

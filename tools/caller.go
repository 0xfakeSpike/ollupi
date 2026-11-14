package tools

// A Caller is who makes a call, and of what: the user, the conversation and where it works, and the item that
// records the call. Every tool is given it with the call.
type Caller struct {
	UserID      string
	ThreadID    string
	WorkspaceID string
	// EnvironmentID is where the conversation runs: a Box the host makes. Empty is the user's environment on this
	// machine.
	EnvironmentID string
	Root          string
	Subagent      bool
	// Role is the role of agent that makes the call.
	Role string
	// Item is the id of the item that records the call; empty outside a call.
	Item string
}

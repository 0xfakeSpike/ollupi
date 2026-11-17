package tools

type Caller struct {
	UserID      string
	ThreadID    string
	WorkspaceID string
	// Empty is the user's environment on this machine.
	EnvironmentID string
	Root          string
	Subagent      bool
	Role          string
	// Empty outside a call.
	Item     string
	Deferred []string
}

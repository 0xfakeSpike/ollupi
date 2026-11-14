package tools

import "context"

type Caller struct {
	UserID      string
	ThreadID    string
	WorkspaceID string
	// EnvironmentID is where the conversation runs: a Box the host makes. Empty is the user's environment on this
	// machine.
	EnvironmentID string
	Root          string
	Subagent      bool
}

type callerKey struct{}

func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func CallerOf(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

type itemKey struct{}

// WithItem gives a call's context the id of the item that records the call.
func WithItem(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, itemKey{}, id)
}

// ItemOf is the id of the item that records the call, or "" outside a call.
func ItemOf(ctx context.Context) string {
	id, _ := ctx.Value(itemKey{}).(string)
	return id
}

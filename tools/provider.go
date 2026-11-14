package tools

import "context"

type Provider interface {
	Name() (id string, description string)
	ToolList(ctx context.Context) ([]*Spec, error)
	Invoke(ctx context.Context, caller Caller, tool string, arguments map[string]any) (Result, error)
}

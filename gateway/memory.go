package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) memoryStore() (*memory.Manager, error) {
	if c.s.memory == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "long-term memory is unavailable")
	}
	return c.s.memory, nil
}

func (c *conn) memoryList(ctx context.Context, p MemoryListParams) (MemoryListResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryListResult{}, nil, err
	}
	entries, err := store.List(c.user, p.Scope, p.WorkspaceID)
	if err != nil {
		return MemoryListResult{}, nil, err
	}
	return MemoryListResult{Data: each(entries, memoryRow)}, nil, nil
}

func (c *conn) memoryStatus(ctx context.Context, _ Empty) (MemoryStatus, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryStatus{}, nil, err
	}
	status, err := store.Status(c.user)
	return memoryStatus(status), nil, err
}

func (c *conn) memoryUpdate(ctx context.Context, p MemoryUpdateParams) (MemoryResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryResult{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := store.Edit(c.user, p.ID, p.Text)
	return MemoryResult{Memory: memoryRow(entry)}, nil, err
}

func (c *conn) memoryMove(ctx context.Context, p MemoryMoveParams) (MemoryResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryResult{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := store.Move(c.user, p.ID)
	return MemoryResult{Memory: memoryRow(entry)}, nil, err
}

func (c *conn) memoryForget(ctx context.Context, p MemoryForgetParams) (Empty, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return Empty{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	return Empty{}, nil, store.Forget(c.user, p.ID)
}

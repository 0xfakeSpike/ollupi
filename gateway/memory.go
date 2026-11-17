package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) memoryList(ctx context.Context, p MemoryListParams) (MemoryListResult, func(), error) {
	entries, err := c.s.service.Memories(c.user, p.Scope, p.WorkspaceID)
	if err != nil {
		return MemoryListResult{}, nil, err
	}
	return MemoryListResult{Data: each(entries, memoryRow)}, nil, nil
}

func (c *conn) memoryStatus(ctx context.Context, _ Empty) (MemoryStatus, func(), error) {
	status, err := c.s.service.MemoryStatus(c.user)
	return memoryStatus(status), nil, err
}

func (c *conn) memoryUpdate(ctx context.Context, p MemoryUpdateParams) (MemoryResult, func(), error) {
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := c.s.service.EditMemory(c.user, p.ID, p.Text)
	return MemoryResult{Memory: memoryRow(entry)}, nil, err
}

func (c *conn) memoryMove(ctx context.Context, p MemoryMoveParams) (MemoryResult, func(), error) {
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := c.s.service.MoveMemory(c.user, p.ID)
	return MemoryResult{Memory: memoryRow(entry)}, nil, err
}

func (c *conn) memoryForget(ctx context.Context, p MemoryForgetParams) (Empty, func(), error) {
	if strings.TrimSpace(p.ID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	return Empty{}, nil, c.s.service.ForgetMemory(c.user, p.ID)
}

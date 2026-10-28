package gateway

import (
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) memoryStore() (*memory.Manager, error) {
	if c.s.memory == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "long-term memory is unavailable")
	}
	return c.s.memory, nil
}

func (c *conn) memoryList(p MemoryListParams) (MemoryListResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryListResult{}, nil, err
	}
	entries, err := store.List(p.Scope, p.WorkspaceID)
	if err != nil {
		return MemoryListResult{}, nil, err
	}
	return MemoryListResult{Data: nonNil(entries)}, nil, nil
}

func (c *conn) memoryStatus(Empty) (repo.MemoryStatus, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return repo.MemoryStatus{}, nil, err
	}
	status, err := store.Status()
	return status, nil, err
}

func (c *conn) memoryUpdate(p MemoryUpdateParams) (MemoryResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryResult{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := store.Edit(p.ID, p.Text)
	return MemoryResult{Memory: entry}, nil, err
}

func (c *conn) memoryMove(p MemoryMoveParams) (MemoryResult, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return MemoryResult{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return MemoryResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	entry, err := store.Move(p.ID)
	return MemoryResult{Memory: entry}, nil, err
}

func (c *conn) memoryForget(p MemoryForgetParams) (Empty, func(), error) {
	store, err := c.memoryStore()
	if err != nil {
		return Empty{}, nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	return Empty{}, nil, store.Forget(p.ID)
}

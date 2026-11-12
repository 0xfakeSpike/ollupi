package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) search(ctx context.Context, p SearchParams) (SearchResult, func(), error) {
	terms := strings.Fields(p.Query)
	if len(terms) == 0 {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "query is required")
	}
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	hits, err := c.s.db.WithContext(ctx).Search(c.user, terms, p.WorkspaceID, p.Limit)
	if err != nil {
		return SearchResult{}, nil, err
	}
	result := SearchResult{Found: found(hits), Memories: []MemoryRow{}}
	if c.s.memory != nil {
		memories, err := c.s.memory.Search(c.user, p.Query, p.WorkspaceID, p.Limit)
		if err != nil {
			return SearchResult{}, nil, err
		}
		result.Memories = each(memories, memoryRow)
	}
	return result, nil, nil
}

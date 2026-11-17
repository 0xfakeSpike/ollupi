package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) search(ctx context.Context, p SearchParams) (SearchResult, func(), error) {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	hits, memories, err := c.s.service.Search(ctx, c.user, p.Query, p.WorkspaceID, p.Limit)
	if err != nil {
		return SearchResult{}, nil, err
	}
	return SearchResult{Found: found(hits), Memories: nonNil(each(memories, memoryRow))}, nil, nil
}

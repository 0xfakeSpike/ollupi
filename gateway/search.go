package gateway

import (
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) search(p SearchParams) (SearchResult, func(), error) {
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
	found, err := c.s.db.Search(terms, p.WorkspaceID, p.Limit)
	if err != nil {
		return SearchResult{}, nil, err
	}
	result := SearchResult{Found: found, Memories: []repo.MemoryRow{}}
	if c.s.memory != nil {
		if result.Memories, err = c.s.memory.Search(p.Query, p.WorkspaceID, p.Limit); err != nil {
			return SearchResult{}, nil, err
		}
	}
	return result, nil, nil
}

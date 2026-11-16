package gateway

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

// A search reads on the shard's one database connection, which every user's turns wait for, so what one query may
// cost is bounded: its length, its words, and how long it runs.
const (
	maxSearchRunes = 200
	maxSearchTerms = 8
	searchTimeout  = 5 * time.Second
)

func (c *conn) search(ctx context.Context, p SearchParams) (SearchResult, func(), error) {
	terms := strings.Fields(p.Query)
	if len(terms) == 0 {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "query is required")
	}
	if utf8.RuneCountInString(p.Query) > maxSearchRunes || len(terms) > maxSearchTerms {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "query must be at most %d characters and %d words", maxSearchRunes, maxSearchTerms)
	}
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "limit must be 1 to %d", maxLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	hits, err := c.s.db.WithContext(ctx).Search(c.user, terms, p.WorkspaceID, p.Limit)
	if errors.Is(err, context.DeadlineExceeded) {
		return SearchResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "the search took too long; try longer or fewer words")
	}
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

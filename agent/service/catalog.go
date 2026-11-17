package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
)

var (
	ErrUnavailable    = errors.New("unavailable on this agent")
	ErrInvalidQuery   = errors.New("invalid query")
	ErrSearchTimedOut = errors.New("the search took too long; try longer or fewer words")
)

// A search reads on the shard's one database connection, which every user's turns wait for, so what one query may
// cost is bounded: its length, its words, and how long it runs.
const (
	MaxSearchRunes = 200
	MaxSearchTerms = 8
	searchTimeout  = 5 * time.Second
)

func (sv *Service) EnsureUser(user string) error { return sv.cfg.DB.EnsureUser(user) }

func (sv *Service) TurnsUnderWay() (int64, error) { return sv.cfg.DB.TurnsUnderWay() }

func (sv *Service) ActiveUsers(since time.Time) (int64, error) { return sv.cfg.DB.ActiveUsers(since) }

func (sv *Service) ThreadPage(ctx context.Context, user, threadID string, before, limit int) (db.ThreadView, bool, error) {
	return sv.cfg.DB.WithContext(ctx).ThreadPage(user, threadID, before, limit)
}

func (sv *Service) Workspaces(ctx context.Context, user string) ([]db.WorkspaceRow, error) {
	return sv.cfg.DB.WithContext(ctx).Workspaces(user)
}

func (sv *Service) Settings(ctx context.Context, user string) (db.SettingsRow, error) {
	return sv.cfg.DB.WithContext(ctx).Settings(user)
}

func (sv *Service) SetRedaction(ctx context.Context, user string, on bool) (db.SettingsRow, error) {
	settings, err := sv.cfg.DB.WithContext(ctx).SetRedaction(user, on)
	if err != nil {
		return db.SettingsRow{}, err
	}
	sv.ev.Publish(event.Event{Kind: event.SettingsChanged, User: user, Settings: &settings})
	return settings, nil
}

func (sv *Service) Search(ctx context.Context, user, query, workspaceID string, limit int) (db.Found, []db.MemoryRow, error) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return db.Found{}, nil, fmt.Errorf("service: %w: query is required", ErrInvalidQuery)
	}
	if utf8.RuneCountInString(query) > MaxSearchRunes || len(terms) > MaxSearchTerms {
		return db.Found{}, nil, fmt.Errorf("service: %w: query must be at most %d characters and %d words", ErrInvalidQuery, MaxSearchRunes, MaxSearchTerms)
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	found, err := sv.cfg.DB.WithContext(ctx).Search(user, terms, workspaceID, limit)
	if errors.Is(err, context.DeadlineExceeded) {
		return db.Found{}, nil, ErrSearchTimedOut
	}
	if err != nil {
		return db.Found{}, nil, err
	}
	if sv.cfg.Memory == nil {
		return found, nil, nil
	}
	memories, err := sv.cfg.Memory.Search(user, query, workspaceID, limit)
	return found, memories, err
}

func (sv *Service) MCPServers(user string) ([]mcp.ServerStatus, error) {
	if sv.cfg.MCP == nil {
		return nil, nil
	}
	mine, err := sv.cfg.MCP.For(user)
	if err != nil {
		return nil, err
	}
	return mine.Statuses(), nil
}

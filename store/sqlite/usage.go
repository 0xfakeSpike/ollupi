package sqlite

import (
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
)

type usageEntryRow struct {
	ThreadID    string
	Seq         int64
	UserID      string
	WorkspaceID string
	TurnID      string
	ItemID      string
	At          time.Time
	Body        string
}

func (r reader) MissingUsage(thread string, after store.UsageKey, limit int) ([]store.UsageEntry, bool, error) {
	query := `SELECT e.thread_id, e.seq, t.user_id, COALESCE(t.workspace_id, '') AS workspace_id, e.turn_id, e.item_id, e.at, e.body
		FROM entries e JOIN threads t ON t.id = e.thread_id
		LEFT JOIN usage u ON u.id = e.thread_id || ':' || e.seq
		WHERE e.kind = 'modelResponded' AND u.id IS NULL AND (e.thread_id, e.seq) > (?, ?)`
	args := []any{after.Thread, after.Seq}
	if thread != "" {
		query += " AND e.thread_id = ?"
		args = append(args, thread)
	}
	query += " ORDER BY e.thread_id, e.seq LIMIT ?"
	args = append(args, limit+1)
	var rows []usageEntryRow
	if err := r.g.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("store: the model responses without usage: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]store.UsageEntry, len(rows))
	for i, row := range rows {
		out[i] = store.UsageEntry{Thread: row.ThreadID, Seq: row.Seq, User: row.UserID, Workspace: row.WorkspaceID,
			Turn: row.TurnID, Item: row.ItemID, At: row.At, Body: []byte(row.Body)}
	}
	return out, more, nil
}

func (r reader) UsageBuckets(user string, q store.UsageQuery) ([]store.UsageBucket, error) {
	key := ""
	var args []any
	switch q.Group {
	case store.UsageByDay:
		key = "strftime('%Y-%m-%d', at, ?)"
		args = append(args, fmt.Sprintf("%+d minutes", q.OffsetMinutes))
	case store.UsageByWorkspace:
		key = "workspace_id"
	case store.UsageByModel:
		key = "model"
	default:
		return nil, fmt.Errorf("store: usage grouped by %q", q.Group)
	}
	query := "SELECT " + key + ` AS key,
		SUM(input_tokens) AS input_tokens, SUM(cached_input_tokens) AS cached_input_tokens,
		SUM(cache_write_input_tokens) AS cache_write_input_tokens, SUM(output_tokens) AS output_tokens
		FROM usage WHERE user_id = ? AND at >= ? AND at < ? GROUP BY key ORDER BY key`
	args = append(args, user, q.From.UTC(), q.To.UTC())
	var rows []struct {
		Key                   string
		InputTokens           int64
		CachedInputTokens     int64
		CacheWriteInputTokens int64
		OutputTokens          int64
	}
	if err := r.g.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: the usage of user %s: %w", user, err)
	}
	out := make([]store.UsageBucket, len(rows))
	for i, row := range rows {
		out[i] = store.UsageBucket{Key: row.Key, Tokens: conversation.Usage{InputTokens: row.InputTokens,
			CachedInputTokens: row.CachedInputTokens, CacheWriteInputTokens: row.CacheWriteInputTokens, OutputTokens: row.OutputTokens}}
	}
	return out, nil
}

func (t *tx) PutUsage(u store.Usage) error {
	err := t.g.Exec(`INSERT INTO usage(id, thread_id, seq, user_id, workspace_id, turn_id, item_id, at, source, model,
		input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		u.ID, u.Thread, u.Seq, u.User, u.Workspace, u.Turn, u.Item, u.At.UTC(), string(u.Source), u.Model,
		u.Tokens.InputTokens, u.Tokens.CachedInputTokens, u.Tokens.CacheWriteInputTokens, u.Tokens.OutputTokens).Error
	if err != nil {
		return fmt.Errorf("store: usage %s: %w", u.ID, err)
	}
	return nil
}

package sqlite

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/store"
)

// presenceByDay splits every stretch at the days of the caller's time zone, so
// a stretch that runs past midnight counts in both days.
func presenceByDay(user string, q store.PresenceQuery) (string, []any) {
	offset := fmt.Sprintf("%+d minutes", q.OffsetMinutes)
	back := fmt.Sprintf("%+d minutes", -q.OffsetMinutes)
	query := `WITH RECURSIVE
		seg(id, s, e) AS (
			SELECT id, datetime(max(started_at, ?)), datetime(min(ended_at, ?))
			FROM presence WHERE user_id = ? AND ended_at > ? AND started_at < ?
		),
		part(id, s, e) AS (
			SELECT id, s, min(e, datetime(date(s, ?), '+1 day', ?)) FROM seg WHERE e > s
			UNION ALL
			SELECT part.id, part.e, min(seg.e, datetime(date(part.e, ?), '+1 day', ?))
			FROM part JOIN seg ON seg.id = part.id WHERE part.e < seg.e
		)
		SELECT date(s, ?) AS key,
			CAST(ROUND(SUM((julianday(e) - julianday(s)) * 86400)) AS INTEGER) AS seconds
		FROM part GROUP BY key ORDER BY key`
	args := []any{q.From.UTC(), q.To.UTC(), user, q.From.UTC(), q.To.UTC(), offset, back, offset, back, offset}
	return query, args
}

func (r reader) PresenceBuckets(user string, q store.PresenceQuery) ([]store.PresenceBucket, error) {
	var (
		query string
		args  []any
	)
	if q.Group == store.PresenceByDay {
		query, args = presenceByDay(user, q)
	} else {
		key := ""
		switch q.Group {
		case store.PresenceByThread:
			key = "thread_id"
		case store.PresenceByTodo:
			key = "CAST(todo_id AS TEXT)"
		case store.PresenceByWorkspace:
			key = "workspace_id"
		case store.PresenceByApp:
			key = "''"
		default:
			return nil, fmt.Errorf("store: presence grouped by %q", q.Group)
		}
		scope := ""
		if q.Group == store.PresenceByThread {
			// The time on no thread is in the app or elsewhere, and belongs to no
			// thread the caller could name.
			scope = " AND thread_id <> ''"
		}
		if q.Group == store.PresenceByApp {
			// The app layer is time with the window in front, with or without a
			// conversation on screen; elsewhere is time in another application.
			scope = " AND state <> 'elsewhere'"
		}
		query = "SELECT " + key + ` AS key,
			CAST(ROUND(SUM((julianday(min(ended_at, ?)) - julianday(max(started_at, ?))) * 86400)) AS INTEGER) AS seconds
			FROM presence WHERE user_id = ? AND ended_at > ? AND started_at < ?` + scope + `
			GROUP BY key ORDER BY key`
		args = []any{q.To.UTC(), q.From.UTC(), user, q.From.UTC(), q.To.UTC()}
	}
	var rows []struct {
		Key     string
		Seconds int64
	}
	if err := r.g.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: the presence of user %s: %w", user, err)
	}
	out := make([]store.PresenceBucket, len(rows))
	for i, row := range rows {
		out[i] = store.PresenceBucket{Key: row.Key, Seconds: row.Seconds}
	}
	return out, nil
}

func (t *tx) PutPresence(p store.Presence) error {
	err := t.g.Exec(`INSERT INTO presence(user_id, id, thread_id, todo_id, workspace_id, state, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, id) DO UPDATE SET ended_at = max(presence.ended_at, excluded.ended_at)`,
		p.User, p.ID, p.Thread, p.Todo, p.Workspace, string(p.State), p.Start.UTC(), p.End.UTC()).Error
	if err != nil {
		return fmt.Errorf("store: presence %s: %w", p.ID, err)
	}
	return nil
}

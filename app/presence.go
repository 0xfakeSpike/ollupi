package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/store"
)

// MaxPresenceBatch is how many stretches of time the desktop client sends at
// once.
const MaxPresenceBatch = 50

// RecordPresence appends stretches of time the user spent in the app. A
// stretch the client sends twice extends the row it already wrote; it is never
// moved, so summing the table stays trustworthy when a client retries.
func (a *App) RecordPresence(ctx context.Context, user string, rows []store.Presence) error {
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > MaxPresenceBatch {
		return fmt.Errorf("%w: at most %d stretches at a time", ErrInvalidPresence, MaxPresenceBatch)
	}
	for i := range rows {
		rows[i].User = user
		if err := validPresence(rows[i]); err != nil {
			return err
		}
	}
	return a.st.Update(ctx, func(tx store.Tx) error {
		for _, p := range rows {
			if err := tx.PutPresence(p); err != nil {
				return err
			}
		}
		return nil
	})
}

func validPresence(p store.Presence) error {
	if p.ID == "" {
		return fmt.Errorf("%w: a stretch needs an id", ErrInvalidPresence)
	}
	if !slices.Contains(store.PresenceStates, p.State) {
		return fmt.Errorf("%w: state must be seen, in_app or elsewhere", ErrInvalidPresence)
	}
	if !p.Start.Before(p.End) {
		return fmt.Errorf("%w: a stretch starts before it ends", ErrInvalidPresence)
	}
	if (p.State == store.PresenceSeen) != (p.Thread != "") {
		return fmt.Errorf("%w: a seen stretch names its thread, and only a seen one does", ErrInvalidPresence)
	}
	return nil
}

// Presence sums the time the user spent in the app over a half-open range,
// grouped by thread, workspace, day or the app as a whole. Only the time
// the app was in front is recorded, so time spent in other applications is
// missing on purpose: the sum is a lower bound on the user's attention. A
// todo's time is the time of the thread it is linked to.
func (a *App) Presence(ctx context.Context, user string, q store.PresenceQuery) ([]store.PresenceBucket, error) {
	if !slices.Contains(store.PresenceGroups, q.Group) {
		return nil, fmt.Errorf("%w: groupBy must be thread, workspace, day or app", ErrInvalidQuery)
	}
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) {
		return nil, fmt.Errorf("%w: from and to are required, and from must be before to", ErrInvalidQuery)
	}
	if q.OffsetMinutes < -24*60 || q.OffsetMinutes > 24*60 {
		return nil, fmt.Errorf("%w: offsetMinutes must be within a day", ErrInvalidQuery)
	}
	var buckets []store.PresenceBucket
	err := a.st.Read(ctx, func(r store.Reader) (err error) { buckets, err = r.PresenceBuckets(user, q); return err })
	return buckets, err
}

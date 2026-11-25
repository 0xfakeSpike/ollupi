package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/store"
)

// Usage sums the user's recorded model calls over a half-open time range,
// grouped by day, workspace, provider, connection or model. The rows are written by the background
// recorder; a call whose turn is still running may therefore be missing until
// its entry is committed and recorded.
func (a *App) Usage(ctx context.Context, user string, q store.UsageQuery) ([]store.UsageBucket, error) {
	if !slices.Contains(store.UsageGroups, q.Group) {
		return nil, fmt.Errorf("%w: groupBy must be one of %q", ErrInvalidQuery, store.UsageGroups)
	}
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) {
		return nil, fmt.Errorf("%w: from and to are required, and from must be before to", ErrInvalidQuery)
	}
	if q.OffsetMinutes < -24*60 || q.OffsetMinutes > 24*60 {
		return nil, fmt.Errorf("%w: offsetMinutes must be within a day", ErrInvalidQuery)
	}
	var buckets []store.UsageBucket
	err := a.st.Read(ctx, func(r store.Reader) (err error) { buckets, err = r.UsageBuckets(user, q); return err })
	return buckets, err
}

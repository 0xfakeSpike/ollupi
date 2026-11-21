package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
)

func usageStart(t *testing.T, s *Store, owner, thread, workspace string) {
	t.Helper()
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		if err := tx.EnsureUser(owner); err != nil {
			return err
		}
		if workspace != "" {
			if err := tx.PutWorkspace(owner, account.Workspace{ID: workspace, Name: workspace, Root: t.TempDir(), Created: now, Updated: now}); err != nil {
				return err
			}
		}
		return tx.CreateThread(store.Thread{User: owner, Thread: account.Thread{ID: thread, Workspace: workspace, Title: "chat", Created: now, Updated: now}})
	})
}

func missing(t *testing.T, s *Store, thread string, after store.UsageKey, limit int) ([]store.UsageEntry, bool) {
	t.Helper()
	var entries []store.UsageEntry
	var more bool
	if err := s.Read(context.Background(), func(r store.Reader) (err error) {
		entries, more, err = r.MissingUsage(thread, after, limit)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return entries, more
}

func buckets(t *testing.T, s *Store, user string, q store.UsageQuery) []store.UsageBucket {
	t.Helper()
	var out []store.UsageBucket
	if err := s.Read(context.Background(), func(r store.Reader) (err error) { out, err = r.UsageBuckets(user, q); return err }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMissingUsagePagesByThreadAndSeq(t *testing.T) {
	s := open(t, t.TempDir())
	usageStart(t, s, "u", "th", "w")
	usageStart(t, s, "u", "other", "w")
	update(t, s, func(tx store.Tx) error {
		if _, err := tx.Append("th",
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r", Usage: conversation.Usage{InputTokens: 10}}},
			conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "summary", Compaction: true, Usage: conversation.Usage{InputTokens: 5}}},
			conversation.Entry{Turn: "t1", Item: "f1", Body: conversation.ModelFailed{}}); err != nil {
			return err
		}
		_, err := tx.Append("other", conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{}},
			conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r", Usage: conversation.Usage{InputTokens: 7}}})
		return err
	})

	page, more := missing(t, s, "th", store.UsageKey{Thread: "th"}, 1)
	if !more || len(page) != 1 || page[0].Seq != 2 || page[0].User != "u" || page[0].Workspace != "w" || page[0].Turn != "t1" || page[0].Item != "" {
		t.Fatalf("the first page %+v, more %v", page, more)
	}
	page, more = missing(t, s, "th", store.UsageKey{Thread: page[0].Thread, Seq: page[0].Seq}, 1)
	if more || len(page) != 1 || page[0].Seq != 3 {
		t.Fatalf("the second page %+v, more %v", page, more)
	}
	all, more := missing(t, s, "", store.UsageKey{}, 10)
	if more || len(all) != 3 || all[0].Thread != "other" || all[1].Thread != "th" || all[2].Thread != "th" {
		t.Errorf("every thread %+v, more %v", all, more)
	}

	// Once a row exists for an entry, the entry is no longer missing.
	update(t, s, func(tx store.Tx) error {
		return tx.PutUsage(store.Usage{ID: "th:2", Thread: "th", Seq: 2, User: "u", At: time.Now().UTC(), Source: store.UsageResponse})
	})
	page, _ = missing(t, s, "th", store.UsageKey{Thread: "th"}, 10)
	if len(page) != 1 || page[0].Seq != 3 {
		t.Errorf("after recording the first %+v", page)
	}
}

func TestPutUsageKeepsTheFirstRowOfAKey(t *testing.T) {
	s := open(t, t.TempDir())
	usageStart(t, s, "u", "th", "w")
	now := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	row := store.Usage{ID: "th:2", Thread: "th", Seq: 2, User: "u", Workspace: "w", At: now,
		Source: store.UsageResponse, Model: "m1", Tokens: conversation.Usage{InputTokens: 10, OutputTokens: 3}}
	update(t, s, func(tx store.Tx) error { return tx.PutUsage(row) })
	row.Tokens = conversation.Usage{InputTokens: 999}
	update(t, s, func(tx store.Tx) error { return tx.PutUsage(row) })
	got := buckets(t, s, "u", store.UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour), Group: store.UsageByModel})
	if len(got) != 1 || got[0].Tokens.InputTokens != 10 || got[0].Tokens.OutputTokens != 3 {
		t.Errorf("the buckets %+v", got)
	}
}

func TestUsageBucketsGroupInTheCallersTimeZone(t *testing.T) {
	s := open(t, t.TempDir())
	usageStart(t, s, "u", "th", "")
	usageStart(t, s, "v", "theirs", "")
	at := func(day int, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }
	rows := []store.Usage{
		{ID: "a", User: "u", Workspace: "w1", At: at(1, 20), Model: "m1", Source: store.UsageResponse,
			Tokens: conversation.Usage{InputTokens: 10, CachedInputTokens: 1, CacheWriteInputTokens: 2, OutputTokens: 3}},
		{ID: "b", User: "u", Workspace: "w1", At: at(1, 23), Model: "m2", Source: store.UsageResponse,
			Tokens: conversation.Usage{InputTokens: 20, OutputTokens: 4}},
		{ID: "c", User: "u", Workspace: "w2", At: at(2, 10), Model: "m1", Source: store.UsageCompaction,
			Tokens: conversation.Usage{InputTokens: 5, CachedInputTokens: 1, CacheWriteInputTokens: 1, OutputTokens: 1}},
		{ID: "d", User: "v", Workspace: "w1", At: at(1, 20), Model: "m1", Source: store.UsageResponse,
			Tokens: conversation.Usage{InputTokens: 1000}},
	}
	update(t, s, func(tx store.Tx) error {
		for _, row := range rows {
			if err := tx.PutUsage(row); err != nil {
				return err
			}
		}
		return nil
	})
	q := store.UsageQuery{From: at(1, 0), To: at(3, 0)}

	sum := func(got []store.UsageBucket, key string) conversation.Usage {
		t.Helper()
		for _, b := range got {
			if b.Key == key {
				return b.Tokens
			}
		}
		t.Fatalf("no bucket %q in %+v", key, got)
		return conversation.Usage{}
	}

	utc := buckets(t, s, "u", store.UsageQuery{From: q.From, To: q.To, Group: store.UsageByDay})
	if len(utc) != 2 {
		t.Fatalf("the UTC days %+v", utc)
	}
	if u := sum(utc, "2026-03-01"); u.InputTokens != 30 || u.CachedInputTokens != 1 || u.CacheWriteInputTokens != 2 || u.OutputTokens != 7 {
		t.Errorf("March 1 in UTC %+v", u)
	}
	if u := sum(utc, "2026-03-02"); u.InputTokens != 5 || u.OutputTokens != 1 {
		t.Errorf("March 2 in UTC %+v", u)
	}

	east := buckets(t, s, "u", store.UsageQuery{From: q.From, To: q.To, Group: store.UsageByDay, OffsetMinutes: 8 * 60})
	if len(east) != 1 {
		t.Fatalf("the days in UTC+8 %+v", east)
	}
	if u := sum(east, "2026-03-02"); u.InputTokens != 35 || u.CachedInputTokens != 2 || u.CacheWriteInputTokens != 3 || u.OutputTokens != 8 {
		t.Errorf("March 2 in UTC+8 %+v", u)
	}

	byWorkspace := buckets(t, s, "u", store.UsageQuery{From: q.From, To: q.To, Group: store.UsageByWorkspace})
	if len(byWorkspace) != 2 || sum(byWorkspace, "w1").InputTokens != 30 || sum(byWorkspace, "w2").InputTokens != 5 {
		t.Errorf("by workspace %+v", byWorkspace)
	}

	byModel := buckets(t, s, "u", store.UsageQuery{From: q.From, To: q.To, Group: store.UsageByModel})
	if len(byModel) != 2 || sum(byModel, "m1").InputTokens != 15 || sum(byModel, "m2").InputTokens != 20 {
		t.Errorf("by model %+v", byModel)
	}

	// The two ends: the entry at "to" belongs to a later query.
	late := buckets(t, s, "u", store.UsageQuery{From: at(2, 0), To: at(2, 10), Group: store.UsageByModel})
	if len(late) != 0 {
		t.Errorf("the half-open end %+v", late)
	}
	err := s.Read(context.Background(), func(r store.Reader) error {
		_, err := r.UsageBuckets("u", store.UsageQuery{From: q.From, To: q.To, Group: "hour"})
		return err
	})
	if err == nil {
		t.Error("an unknown grouping was accepted")
	}
}

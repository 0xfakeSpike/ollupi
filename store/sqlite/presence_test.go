package sqlite

import (
	"context"
	"github.com/0xfakeSpike/ollupi/account"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/store"
)

func putPresence(t *testing.T, s *Store, user string, rows ...store.Presence) {
	t.Helper()
	update(t, s, func(tx store.Tx) error {
		if err := tx.EnsureUser(user, account.NewEnvironment("This computer", "local", nil)); err != nil {
			return err
		}
		for _, p := range rows {
			p.User = user
			if err := tx.PutPresence(p); err != nil {
				return err
			}
		}
		return nil
	})
}

func presenceOf(t *testing.T, s *Store, user string, q store.PresenceQuery) []store.PresenceBucket {
	t.Helper()
	var out []store.PresenceBucket
	if err := s.Read(context.Background(), func(r store.Reader) (err error) { out, err = r.PresenceBuckets(user, q); return err }); err != nil {
		t.Fatal(err)
	}
	return out
}

func at(hour, minute int) time.Time {
	return time.Date(2026, 3, 1, hour, minute, 0, 0, time.UTC)
}

func TestPresenceExtendsAStretchItSeesAgain(t *testing.T) {
	s := open(t, t.TempDir())
	putPresence(t, s, "u", store.Presence{ID: "a", Thread: "th", State: store.PresenceSeen, Start: at(9, 0), End: at(9, 30)})
	putPresence(t, s, "u", store.Presence{ID: "a", Thread: "th", State: store.PresenceSeen, Start: at(9, 0), End: at(9, 20)})
	putPresence(t, s, "u", store.Presence{ID: "a", Thread: "th", State: store.PresenceSeen, Start: at(9, 0), End: at(10, 0)})
	got := presenceOf(t, s, "u", store.PresenceQuery{From: at(8, 0), To: at(11, 0), Group: store.PresenceByThread})
	if len(got) != 1 || got[0].Key != "th" || got[0].Seconds != 3600 {
		t.Fatalf("the stretch should be one hour of thread time, got %+v", got)
	}
}

func TestPresenceClipsToTheRange(t *testing.T) {
	s := open(t, t.TempDir())
	putPresence(t, s, "u", store.Presence{ID: "a", Thread: "th", State: store.PresenceSeen, Start: at(9, 0), End: at(10, 0)})
	got := presenceOf(t, s, "u", store.PresenceQuery{From: at(9, 15), To: at(9, 45), Group: store.PresenceByApp})
	if len(got) != 1 || got[0].Key != "" || got[0].Seconds != 1800 {
		t.Fatalf("the range should clip the stretch to half an hour, got %+v", got)
	}
}

func TestPresenceGroupsByTodoAndSkipsOtherStretches(t *testing.T) {
	s := open(t, t.TempDir())
	putPresence(t, s, "u",
		store.Presence{ID: "a", Thread: "th", Todo: 7, State: store.PresenceSeen, Start: at(9, 0), End: at(9, 30)},
		store.Presence{ID: "b", Thread: "th2", Todo: 7, State: store.PresenceSeen, Start: at(9, 30), End: at(9, 45)},
		store.Presence{ID: "c", State: store.PresenceInApp, Start: at(9, 45), End: at(10, 0)},
		store.Presence{ID: "d", State: store.PresenceElsewhere, Start: at(10, 0), End: at(10, 30)})
	got := presenceOf(t, s, "u", store.PresenceQuery{From: at(8, 0), To: at(11, 0), Group: store.PresenceByTodo})
	if len(got) != 2 {
		t.Fatalf("todo 7 and the app with no todo, the elsewhere stretch in neither: %+v", got)
	}
	for _, b := range got {
		switch b.Key {
		case "7":
			if b.Seconds != 45*60 {
				t.Fatalf("todo 7 across two threads should be 45 minutes, got %d", b.Seconds)
			}
		case "0":
			if b.Seconds != 15*60 {
				t.Fatalf("the stretch with no todo should be 15 minutes, got %d", b.Seconds)
			}
		default:
			t.Fatalf("unexpected bucket %+v", b)
		}
	}
}

func TestPresenceSplitsAtLocalMidnight(t *testing.T) {
	s := open(t, t.TempDir())
	// 15:00 to 17:00 UTC is 23:00 to 01:00 in a zone eight hours east of UTC.
	putPresence(t, s, "u", store.Presence{ID: "a", Thread: "th", State: store.PresenceSeen, Start: at(15, 0), End: at(17, 0)})
	got := presenceOf(t, s, "u", store.PresenceQuery{From: at(0, 0), To: at(23, 0), Group: store.PresenceByDay, OffsetMinutes: 8 * 60})
	want := map[string]int64{"2026-03-01": 3600, "2026-03-02": 3600}
	if len(got) != len(want) {
		t.Fatalf("the night should fall in two days, got %+v", got)
	}
	for _, b := range got {
		if want[b.Key] != b.Seconds {
			t.Fatalf("day %s should be %d seconds, got %d", b.Key, want[b.Key], b.Seconds)
		}
	}
}

func TestPresenceLeavesOutElsewhereInEveryGrouping(t *testing.T) {
	s := open(t, t.TempDir())
	putPresence(t, s, "u",
		store.Presence{ID: "a", Thread: "th", Workspace: "w", State: store.PresenceSeen, Start: at(9, 0), End: at(9, 30)},
		store.Presence{ID: "b", Workspace: "w", State: store.PresenceInApp, Start: at(9, 30), End: at(9, 40)},
		store.Presence{ID: "c", Workspace: "w", State: store.PresenceElsewhere, Start: at(9, 40), End: at(10, 40)})
	for _, group := range []store.PresenceGroup{store.PresenceByApp, store.PresenceByDay, store.PresenceByWorkspace, store.PresenceByTodo} {
		got := presenceOf(t, s, "u", store.PresenceQuery{From: at(8, 0), To: at(11, 0), Group: group})
		if len(got) != 1 || got[0].Seconds != 40*60 {
			t.Errorf("by %s: the conversation and the app without one, 40 minutes, got %+v", group, got)
		}
	}
}

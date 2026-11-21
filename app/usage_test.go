package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestUsageSumsWhatTheRecorderWrote(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	for i, input := range []int64{10, 20} {
		row := store.Usage{ID: fmt.Sprintf("row%d", i), Thread: "th", User: "u", Workspace: "w",
			At: at.Add(time.Duration(i) * time.Minute), Source: store.UsageResponse, Model: "m",
			Tokens: conversation.Usage{InputTokens: input, OutputTokens: 1}}
		if err := f.st.Update(f.ctx, func(tx store.Tx) error { return tx.PutUsage(row) }); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.a.Usage(f.ctx, "u", store.UsageQuery{From: at.Add(-time.Hour), To: at.Add(time.Hour), Group: store.UsageByDay})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "2026-03-01" || got[0].Tokens.InputTokens != 30 || got[0].Tokens.OutputTokens != 2 {
		t.Errorf("the buckets %+v", got)
	}
	// Another user's calls are not part of this user's answer.
	if theirs, err := f.a.Usage(f.ctx, "v", store.UsageQuery{From: at.Add(-time.Hour), To: at.Add(time.Hour), Group: store.UsageByDay}); err != nil || len(theirs) != 0 {
		t.Errorf("the other user's buckets %+v, %v", theirs, err)
	}
}

func TestUsageRefusesAQueryItCannotAnswer(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	for name, q := range map[string]store.UsageQuery{
		"no grouping":      {From: at, To: at.Add(time.Hour)},
		"unknown grouping": {From: at, To: at.Add(time.Hour), Group: "hour"},
		"no start":         {To: at.Add(time.Hour), Group: store.UsageByDay},
		"no end":           {From: at, Group: store.UsageByDay},
		"backwards":        {From: at.Add(time.Hour), To: at, Group: store.UsageByDay},
		"offset too far":   {From: at, To: at.Add(time.Hour), Group: store.UsageByDay, OffsetMinutes: 24*60 + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.a.Usage(f.ctx, "u", q); !errors.Is(err, ErrInvalidQuery) {
				t.Errorf("the query was answered with %v", err)
			}
		})
	}
}

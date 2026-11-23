package gateway

import (
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/store"
)

func presenceDay() (time.Time, func(hour, minute int) time.Time) {
	day := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	return day, func(hour, minute int) time.Time {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
}

func TestPresenceRecordsAndSumsWhatTheClientSaw(t *testing.T) {
	_, c := connect(t)
	day, at := presenceDay()

	c.call("presence/record", PresenceRecordParams{Rows: []PresenceInput{
		{ID: "a", ThreadID: "th", WorkspaceID: "w", State: store.PresenceSeen, Start: at(9, 0), End: at(9, 30)},
		{ID: "b", ThreadID: "th", WorkspaceID: "w", State: store.PresenceSeen, Start: at(9, 30), End: at(9, 45)},
		{ID: "c", State: store.PresenceElsewhere, Start: at(9, 45), End: at(10, 45)},
	}}, &Empty{})

	// The same stretch sent again with an earlier end keeps the later one.
	c.call("presence/record", PresenceRecordParams{Rows: []PresenceInput{
		{ID: "a", ThreadID: "th", WorkspaceID: "w", State: store.PresenceSeen, Start: at(9, 0), End: at(9, 20)},
	}}, &Empty{})

	from, to := day, day.Add(24*time.Hour)
	var threads PresenceSummaryResult
	c.call("presence/summary", PresenceSummaryParams{From: from, To: to, GroupBy: store.PresenceByThread}, &threads)
	if len(threads.Data) != 1 || threads.Data[0].Key != "th" || threads.Data[0].Seconds != 45*60 {
		t.Fatalf("the thread should hold 30 and 15 minutes, got %+v", threads.Data)
	}

	var app PresenceSummaryResult
	c.call("presence/summary", PresenceSummaryParams{From: from, To: to, GroupBy: store.PresenceByApp}, &app)
	if len(app.Data) != 1 || app.Data[0].Key != "" || app.Data[0].Seconds != 45*60 {
		t.Fatalf("the app is the same 45 minutes, not the hour elsewhere, got %+v", app.Data)
	}
}

func TestPresenceRefusesAStretchThatCannotBeStored(t *testing.T) {
	_, c := connect(t)
	day, _ := presenceDay()

	sent := c.reply("presence/record", PresenceRecordParams{Rows: []PresenceInput{
		{ID: "x", State: store.PresenceSeen, Start: day, End: day.Add(time.Minute)},
	}})
	if sent.Error == nil || sent.Error.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("a seen stretch with no thread should be refused as invalid, got %+v", sent.Error)
	}
}

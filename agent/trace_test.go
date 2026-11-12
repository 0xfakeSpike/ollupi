package agent_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// spans records what this package's tests trace. The global TracerProvider takes over the tracers made before it only
// the first time it is set, so it is set once.
var spans = sync.OnceValue(func() *tracetest.SpanRecorder {
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	return recorder
})

func TestEveryRunOfATurnJoinsTheTurnsTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	recorder := spans()
	fake := &callingModel{tool: "notes__write", args: `{"text":"buy milk"}`}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ag, db := started(ctx, t, agent.Config{Providers: []tools.Provider{noteTools{&atomic.Int32{}}}}, srv.URL)
	w, err := ag.AddWorkspace(ctx, repo.DefaultUser, t.TempDir(), "app")
	if err != nil {
		t.Fatal(err)
	}
	th, err := ag.StartThread(repo.DefaultUser, w.ID, "", "", permission.Settings{ApprovalPolicy: string(permission.Untrusted)})
	if err != nil {
		t.Fatal(err)
	}
	rctx, request := otel.Tracer("test").Start(ctx, "turn/start")
	turn, err := ag.StartTurn(rctx, repo.DefaultUser, th.ID, agent.Input{Text: "记一下买牛奶"})
	request.End()
	if err != nil {
		t.Fatal(err)
	}
	until := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			select {
			case <-ctx.Done():
				t.Fatalf("never %s", what)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	until("asked for approval", func() bool {
		row, err := db.Turn(turn.ID)
		return err == nil && row.Status == repo.TurnWaitingOnApproval
	})
	asked, err := ag.PendingRequests(repo.DefaultUser, th.ID)
	if err != nil || len(asked) != 1 || asked[0].Item.Approval == nil {
		t.Fatalf("pending requests = %+v, %v", asked, err)
	}
	if err := ag.ResolveApproval(repo.DefaultUser, th.ID, asked[0].Item.Approval.ID, repo.DecisionAccept, repo.DefaultUser); err != nil {
		t.Fatal(err)
	}

	traceID := strings.ReplaceAll(turn.ID, "-", "")
	ofTurn := func() map[string][]sdktrace.ReadOnlySpan {
		byName := map[string][]sdktrace.ReadOnlySpan{}
		for _, s := range recorder.Ended() {
			if s.SpanContext().TraceID().String() == traceID {
				byName[s.Name()] = append(byName[s.Name()], s)
			}
		}
		return byName
	}
	until("recorded both runs", func() bool { return len(ofTurn()["ollupi.turn.run"]) == 2 })
	traced := ofTurn()
	for name, want := range map[string]int{"ollupi.turn.wait": 2, "chat demo": 2, "execute_tool notes__write": 1, "HTTP POST": 2} {
		if got := len(traced[name]); got != want {
			t.Errorf("%d %q spans in the turn's trace, want %d", got, name, want)
		}
	}
	runs, waits := traced["ollupi.turn.run"], traced["ollupi.turn.wait"]
	if !runs[0].Parent().Equal(runs[1].Parent()) || runs[0].Parent().TraceID().String() != traceID {
		t.Errorf("the runs do not share the turn's parent: %v, %v", runs[0].Parent(), runs[1].Parent())
	}
	if again := waits[1].StartTime(); again.Before(runs[0].EndTime()) {
		t.Errorf("the second wait starts at %v, before the first run ended at %v", again, runs[0].EndTime())
	}
	if tool := traced["execute_tool notes__write"][0]; tool.Parent().SpanID() != runs[1].SpanContext().SpanID() {
		t.Error("the tool call is not in the run after the approval")
	}
	for _, chat := range traced["chat demo"] {
		if chat.Parent().SpanID() != runs[0].SpanContext().SpanID() && chat.Parent().SpanID() != runs[1].SpanContext().SpanID() {
			t.Errorf("a model call is outside the runs: parent %v", chat.Parent())
		}
	}

	var linked bool
	for _, s := range recorder.Ended() {
		if s.Name() != "turn/start" {
			continue
		}
		for _, l := range s.Links() {
			linked = linked || l.SpanContext.TraceID().String() == traceID
		}
	}
	if !linked {
		t.Error("the request that started the turn does not lead to its trace")
	}
}

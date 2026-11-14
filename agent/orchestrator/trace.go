package orchestrator

import (
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// The orchestrator traces through the global TracerProvider, which the host sets up with its exporter and sampler; until it
// does, tracing costs nothing. A turn's runs and waits are the runtime's spans, in the turn's trace.
var tracer = otel.Tracer("github.com/0xfakeSpike/ollupi/agent/orchestrator")

// turnTrace is the trace every run of a turn joins: a turn runs once per slot it takes, perhaps in several processes as
// the shard changes hands, and holds no span open in between. Its trace ID is the turn's ID, so a log line naming the
// turn leads to its trace, and a sampler that decides on the trace ID, such as TraceIDRatioBased, keeps or drops all
// of a turn. The parent it names is never recorded: the runs and waits are its children, side by side.
func turnTrace(turnID string) trace.SpanContext {
	id, err := uuid.Parse(turnID)
	if err != nil {
		return trace.SpanContext{}
	}
	var spanID trace.SpanID
	copy(spanID[:], id[8:])
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID(id), SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true})
}

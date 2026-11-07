package testenv

import (
	"context"
	"testing"

	"github.com/google/uuid"
	oteltrace "go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/telemetry"
)

// CommandContext returns the test context with the request ID and sampled span
// context that the server root command attaches to each command.
func CommandContext(t *testing.T) context.Context {
	t.Helper()
	traceSource, spanSource := uuid.New(), uuid.New()
	var spanID oteltrace.SpanID
	copy(spanID[:], spanSource[:len(spanID)])
	spanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: oteltrace.TraceID(traceSource), SpanID: spanID, TraceFlags: oteltrace.FlagsSampled,
		TraceState: oteltrace.TraceState{}, Remote: false,
	})
	return telemetry.WithRequestMetadata(oteltrace.ContextWithSpanContext(t.Context(), spanContext), uuid.NewString())
}

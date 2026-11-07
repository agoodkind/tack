package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	oteltrace "go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/telemetry"
)

// CommandContext returns the test context with the request ID and sampled span context that the server root command attaches to each command.
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

// RequireCommandCorrelation requires each event to store the request ID and trace ID from the _meta object of the JSON command output.
func RequireCommandCorrelation(t *testing.T, output string, events []audit.Event) {
	t.Helper()
	requestID, traceID := CommandCorrelation(t, output)
	if len(events) == 0 {
		t.Fatalf("command with request ID %s does not have any audit events", requestID)
	}
	for _, event := range events {
		if event.Actor.RequestID != requestID || event.Context.RequestID != requestID || event.Context.TraceID != traceID {
			t.Fatalf("event %s %s request IDs %q and %q and trace ID %q, want request ID %s and trace ID %s",
				event.EventID, event.Verb, event.Actor.RequestID, event.Context.RequestID, event.Context.TraceID, requestID, traceID)
		}
	}
}

// CommandCorrelation returns the request ID and trace ID from the _meta object of the JSON command output.
func CommandCorrelation(t *testing.T, output string) (string, string) {
	t.Helper()
	var document response.JSONDocument
	if err := json.NewDecoder(strings.NewReader(output)).Decode(&document); err != nil {
		t.Fatalf("decode the JSON command output: %v\n%s", err, output)
	}
	if document.Meta.RequestID == "" || document.Meta.TraceID == "" {
		t.Fatalf("command _meta = %+v, want a request ID and a trace ID\n%s", document.Meta, output)
	}
	return document.Meta.RequestID, document.Meta.TraceID
}

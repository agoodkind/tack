package response

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// ctxWithSpan returns a context carrying a valid span context plus a request
// id, so FromContext resolves non-empty trace_id, span_id, and request_id
// without a configured tracer provider.
func ctxWithSpan(t *testing.T) context.Context {
	t.Helper()
	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
	})
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)
	return telemetry.WithRequestMetadata(ctx, "req-123")
}

func TestFromContextProjectsIDs(t *testing.T) {
	meta := FromContext(ctxWithSpan(t))
	if meta.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace_id = %q", meta.TraceID)
	}
	if meta.SpanID != "0123456789abcdef" {
		t.Fatalf("span_id = %q", meta.SpanID)
	}
	if meta.RequestID != "req-123" {
		t.Fatalf("request_id = %q", meta.RequestID)
	}
}

func TestHeaderLineOmitsEmptyAndMarks(t *testing.T) {
	if line := (Metadata{}).HeaderLine(); line != "" {
		t.Fatalf("empty metadata header = %q", line)
	}
	line := Metadata{TraceID: "t", SpanID: "s", RequestID: "r"}.HeaderLine()
	if !strings.HasPrefix(line, headerMarker) {
		t.Fatalf("header missing marker: %q", line)
	}
	for _, want := range []string{"trace_id=t", "span_id=s", "request_id=r"} {
		if !strings.Contains(line, want) {
			t.Fatalf("header %q missing %q", line, want)
		}
	}
}

func TestTextPrependsHeaderOnce(t *testing.T) {
	meta := Metadata{TraceID: "t"}
	got := meta.Text("body")
	if !strings.HasPrefix(got, headerMarker+" trace_id=t\nbody") {
		t.Fatalf("text = %q", got)
	}
	// A body already stamped is not stamped again.
	if again := meta.Text(got); again != got {
		t.Fatalf("double stamp: %q", again)
	}
}

func TestJSONWrapsPayloadUnderResult(t *testing.T) {
	payload, err := json.Marshal(map[string]int{"count": 2})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	body, err := Marshal(ctxWithSpan(t), payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Meta   Metadata        `json:"_meta"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Meta.TraceID == "" {
		t.Fatalf("_meta.trace_id empty")
	}
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(doc.Result, &result); err != nil {
		t.Fatalf("result unmarshal: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestMarshalRejectsInvalidPayload(t *testing.T) {
	if _, err := Marshal(context.Background(), []byte("not json")); err == nil {
		t.Fatal("expected error for invalid payload")
	}
	if _, err := Marshal(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty payload")
	}
}

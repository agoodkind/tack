// Package response renders operator CLI output with the run's trace
// correlation metadata attached, so a line of output can be tied back to the
// slog and OTel trail by trace_id. It projects the gklog correlation context
// already re-exported through internal/telemetry; it adds no new dependency
// and no separate tracing stack.
package response

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// headerMarker prefixes the one-line text header. A body already beginning
// with it carries a header from an inner producer, so callers skip stamping a
// second one and the original ids survive end to end.
const headerMarker = "#meta"

// Metadata is the caller-visible subset of the trace correlation context.
type Metadata struct {
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
}

// FromContext projects the active trace correlation context. TraceID and
// SpanID come from the OTel span context; RequestID comes from the request
// metadata value. Each is empty when absent.
func FromContext(ctx context.Context) Metadata {
	return Metadata{
		RequestID: strings.TrimSpace(telemetry.RequestID(ctx)),
		TraceID:   telemetry.TraceID(ctx),
		SpanID:    telemetry.SpanID(ctx),
	}
}

// Empty reports whether no correlation field is set.
func (m Metadata) Empty() bool {
	return m.RequestID == "" && m.TraceID == "" && m.SpanID == ""
}

// HeaderLine renders the single text header line, omitting empty fields. It
// returns "" when no field is set so callers can skip an empty header.
func (m Metadata) HeaderLine() string {
	parts := make([]string, 0, 3)
	if m.TraceID != "" {
		parts = append(parts, "trace_id="+m.TraceID)
	}
	if m.SpanID != "" {
		parts = append(parts, "span_id="+m.SpanID)
	}
	if m.RequestID != "" {
		parts = append(parts, "request_id="+m.RequestID)
	}
	if len(parts) == 0 {
		return ""
	}
	return headerMarker + " " + strings.Join(parts, " ")
}

// Text renders body with the metadata header line prepended. A body that
// already starts with the header marker is returned unchanged.
func (m Metadata) Text(body string) string {
	if strings.HasPrefix(body, headerMarker) {
		return body
	}
	header := m.HeaderLine()
	if header == "" {
		return body
	}
	if body == "" {
		return header + "\n"
	}
	return header + "\n" + body
}

// Text renders body with the current context's metadata header prepended.
func Text(ctx context.Context, body string) string {
	return FromContext(ctx).Text(body)
}

// JSONDocument is the operator CLI's top-level JSON contract: correlation
// metadata under _meta and the command payload under result.
type JSONDocument struct {
	Meta   Metadata        `json:"_meta"`
	Result json.RawMessage `json:"result"`
}

// JSON wraps an already-encoded JSON payload in the envelope. It errors on an
// empty or invalid payload so a malformed body never reaches the operator.
func JSON(ctx context.Context, payload []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("response: empty json payload")
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("response: invalid json payload")
	}
	doc := JSONDocument{
		Meta:   FromContext(ctx),
		Result: append(json.RawMessage(nil), trimmed...),
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("response: encode json envelope: %w", err)
	}
	return append(body, '\n'), nil
}

// MarshalJSON encodes value and wraps it in the envelope in one step.
func MarshalJSON(ctx context.Context, value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("response: encode value: %w", err)
	}
	return JSON(ctx, payload)
}

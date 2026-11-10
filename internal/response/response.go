// Package response adds trace correlation metadata to operator command
// output. The trace_id value identifies related slog log records and
// OpenTelemetry (OTel) traces. It uses the existing gklog correlation
// context that telemetry re-exports. It does not add dependencies or
// initialize a separate tracing system.
package response

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// headerMarker starts the single text header line. Text preserves a body
// that starts with this marker, including an inner producer's original
// identifiers, without adding another header.
const headerMarker = "#meta"

// Metadata provides callers with a subset of the trace correlation fields.
type Metadata struct {
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
}

// FromContext reads the active trace correlation metadata from ctx.
//
// Each field contains an empty string when its source value is absent.
//
//   - TraceID and SpanID use the OpenTelemetry span context.
//   - RequestID uses the request metadata value after trimming whitespace.
func FromContext(ctx context.Context) Metadata {
	return Metadata{
		RequestID: strings.TrimSpace(telemetry.RequestID(ctx)),
		TraceID:   telemetry.TraceID(ctx),
		SpanID:    telemetry.SpanID(ctx),
	}
}

// Empty reports whether all correlation fields contain empty strings.
//
// It treats whitespace as a value.
//
//	empty := (Metadata{RequestID: " "}).Empty() // empty is false.
func (m Metadata) Empty() bool {
	return m.RequestID == "" && m.TraceID == "" && m.SpanID == ""
}

// HeaderLine renders one text header line and omits empty fields.
//
// It returns "" when every field contains an empty string. Callers can
// skip the header in that case.
//
//   - The header starts with #meta.
//   - Each populated field uses key=value form.
//   - Spaces separate populated fields.
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

// Text adds the metadata header line before body.
//
// It returns body unchanged when body already starts with #meta.
//
//   - It returns body unchanged when the metadata header is empty.
//   - It adds a newline after the header, including when body is empty.
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

// Text adds the current context's metadata header before body.
//
// It reads the metadata with FromContext and applies Metadata.Text.
//
//   - It preserves body when body starts with #meta.
//   - It preserves body when the context does not provide metadata.
func Text(ctx context.Context, body string) string {
	return FromContext(ctx).Text(body)
}

// JSONDocument defines the operator command's top-level JSON response.
// It stores correlation metadata under _meta and the command payload
// under result.
type JSONDocument struct {
	Meta   Metadata        `json:"_meta"`
	Result json.RawMessage `json:"result"`
}

// Marshal adds the metadata envelope around an encoded JSON payload.
//
// It returns an error for an empty or invalid payload instead of returning
// a malformed response to the operator.
//
//   - _meta contains the current context's correlation metadata.
//   - result contains the payload after trimming surrounding whitespace.
//   - Successful output uses indented JSON and ends with a newline.
func Marshal(ctx context.Context, payload []byte) ([]byte, error) {
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
		slog.ErrorContext(ctx, "response.encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("response: encode json envelope: %w", err)
	}

	return append(body, '\n'), nil
}

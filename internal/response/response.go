// Package response adds trace correlation metadata to operator CLI output.
// The trace_id links output to slog records and OTel traces. The package
// reads the gklog correlation context through internal/telemetry.
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

// headerMarker prefixes the one-line text header. Callers preserve an
// existing header and its original IDs instead of adding another header.
const headerMarker = "#meta"

// Metadata is the caller-visible subset of the trace correlation context.
type Metadata struct {
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
}

// FromContext reads the active trace correlation context. TraceID and
// SpanID come from the OTel span context; RequestID comes from the request
// metadata value. Each field is empty when absent.
func FromContext(ctx context.Context) Metadata {
	return Metadata{
		RequestID: strings.TrimSpace(telemetry.RequestID(ctx)),
		TraceID:   telemetry.TraceID(ctx),
		SpanID:    telemetry.SpanID(ctx),
	}
}

// Empty reports whether every correlation field is empty.
func (m Metadata) Empty() bool {
	return m.RequestID == "" && m.TraceID == "" && m.SpanID == ""
}

// HeaderLine renders the single text header line and omits empty fields.
// It returns an empty string when every field is empty.
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

// Text prepends the metadata header line to body unless body already
// starts with the header marker.
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

// Text prepends the current context's metadata header to body.
func Text(ctx context.Context, body string) string {
	return FromContext(ctx).Text(body)
}

// JSONDocument defines the operator CLI's top-level JSON contract:
// correlation metadata under _meta and the command payload under result.
type JSONDocument struct {
	Meta   Metadata        `json:"_meta"`
	Result json.RawMessage `json:"result"`
}

// Marshal wraps an encoded JSON payload in the envelope. It rejects
// empty or invalid payloads before rendering operator output.
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

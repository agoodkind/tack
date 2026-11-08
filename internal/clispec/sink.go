package clispec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// Result constrains the typed values an operation emits as JSON. Embedding
// ResultMarker satisfies it without boilerplate, mirroring Input, so emit never
// takes a bare any.
type Result interface {
	isClispecResult()
}

// ReadResult supplies the returned identities of a typed read result.
type ReadResult interface {
	Result
	AuditReadTargets() ([]audit.Target, error)
}

type readTargetSink interface {
	addReadTargets([]audit.Target)
}

// ResultMarker embeds into a result struct to satisfy Result.
type ResultMarker struct{}

func (ResultMarker) isClispecResult() {}

// ResultSink is how a work function returns output. Both methods carry the
// run's trace correlation: WriteJSON wraps an encoded payload under result with
// a _meta block, WriteText prepends one header line with trace_id, span_id, and
// request_id. The payload is pre-encoded [json.RawMessage], so the sink never
// takes a bare any.
type ResultSink interface {
	WriteJSON(ctx context.Context, payload json.RawMessage) error
	WriteText(ctx context.Context, body string) error
}

// WriteJSONValue marshals a typed result and writes it through the sink.
//
// The function also calls AuditReadTargets when value implements ReadResult
// and the sink collects read identities. Serialization or target-collection
// errors prevent the sink write.
//
// Layout:
//
//	JSON payload:    json.Marshal(value)
//	Read identities: value.AuditReadTargets() for a collecting read sink
func WriteJSONValue(ctx context.Context, sink ResultSink, value Result) error {
	body, err := json.Marshal(value)
	if err != nil {
		slog.ErrorContext(ctx, "clispec.marshal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clispec: marshal result: %w", err)
	}
	if collector, ok := sink.(readTargetSink); ok {
		if result, ok := value.(ReadResult); ok {
			targets, err := result.AuditReadTargets()
			if err != nil {
				slog.ErrorContext(ctx, "clispec.read_targets_failed", slog.String("err", err.Error()))
				return fmt.Errorf("clispec: collect read targets: %w", err)
			}
			collector.addReadTargets(targets)
		}
	}
	if err := sink.WriteJSON(ctx, body); err != nil {
		return fmt.Errorf("clispec: write result: %w", err)
	}
	return nil
}

// CLISink writes to the factory's out stream in the operator's chosen format.
type CLISink struct {
	f     *cli.Factory
	wrote bool
}

// NewCLISink builds a sink that writes through f.
//
// The sink reads the selected output format from f when it writes. It adds
// available metadata before its first text result.
//
// Example:
//
//	sink := NewCLISink(factory)
//	err := sink.WriteJSON(ctx, json.RawMessage(`{"count":2}`))
func NewCLISink(f *cli.Factory) *CLISink {
	return &CLISink{f: f, wrote: false}
}

// WriteJSON writes a JSON value in the selected CLI output format.
//
// JSON mode wraps the value with response metadata. Text mode indents the
// value and adds available metadata before the first text result.
//
// Layout:
//
//	JSON: {"_meta": {...}, "result": <payload>}
//	Text: #meta ... followed by indented <payload>
func (s *CLISink) WriteJSON(ctx context.Context, payload json.RawMessage) error {
	body, err := RenderJSON(ctx, s.f.OutputFormat(), payload)
	if err != nil {
		return err
	}
	if s.f.OutputFormat() == cli.FormatJSON {
		return s.writeRaw(ctx, body)
	}
	return s.writeText(ctx, string(body))
}

// WriteText writes a string in the selected CLI output format.
//
// JSON mode encodes body as the envelope's result string. Text mode writes
// body with available metadata before the first result and ensures a final
// newline.
//
// Layout:
//
//	JSON result: "<body>"
//	Text body:   body
func (s *CLISink) WriteText(ctx context.Context, body string) error {
	if s.f.OutputFormat() == cli.FormatJSON {
		quoted, err := json.Marshal(body)
		if err != nil {
			slog.ErrorContext(ctx, "clispec.quote_failed", slog.String("err", err.Error()))
			return fmt.Errorf("clispec: quote: %w", err)
		}
		out, err := response.Marshal(ctx, quoted)
		if err != nil {
			slog.ErrorContext(ctx, "clispec.envelope_failed", slog.String("err", err.Error()))
			return fmt.Errorf("clispec: envelope: %w", err)
		}
		return s.writeRaw(ctx, out)
	}
	return s.writeText(ctx, body)
}

// writeText stamps the trace header on the first write only.
func (s *CLISink) writeText(ctx context.Context, body string) error {
	out := body
	if !s.wrote {
		out = response.Text(ctx, body)
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return s.writeRaw(ctx, []byte(out))
}

func (s *CLISink) writeRaw(ctx context.Context, b []byte) error {
	s.wrote = true
	if _, err := s.f.Out.Write(b); err != nil {
		slog.ErrorContext(ctx, "clispec.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clispec: write output: %w", err)
	}
	return nil
}

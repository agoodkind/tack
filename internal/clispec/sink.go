package clispec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// Result constrains the typed values an operation emits as JSON. Embedding
// ResultMarker satisfies it without boilerplate, mirroring Input, so emit never
// takes a bare any.
type Result interface {
	isClispecResult()
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
func WriteJSONValue(ctx context.Context, sink ResultSink, value Result) error {
	body, err := json.Marshal(value)
	if err != nil {
		slog.ErrorContext(ctx, "clispec.marshal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clispec: marshal result: %w", err)
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
func NewCLISink(f *cli.Factory) *CLISink {
	return &CLISink{f: f, wrote: false}
}

// WriteJSON renders a JSON payload. In json mode it is the envelope payload; in
// text mode it is indented JSON under the trace header.
func (s *CLISink) WriteJSON(ctx context.Context, payload json.RawMessage) error {
	if s.f.OutputFormat() == cli.FormatJSON {
		body, err := response.Marshal(ctx, payload)
		if err != nil {
			slog.ErrorContext(ctx, "clispec.envelope_failed", slog.String("err", err.Error()))
			return fmt.Errorf("clispec: envelope: %w", err)
		}
		return s.writeRaw(ctx, body)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, payload, "", "  "); err != nil {
		slog.ErrorContext(ctx, "clispec.indent_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clispec: indent: %w", err)
	}
	return s.writeText(ctx, buf.String())
}

// WriteText renders a human line. In json mode the line becomes the envelope's
// result string so json output stays valid JSON.
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

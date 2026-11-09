package clispec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// Result constrains the typed values an operation emits as JSON. A result
// struct embeds [ResultMarker] to satisfy Result, just as an input struct
// embeds [InputMarker] to satisfy [Input]. [WriteJSONValue] accepts Result
// rather than any.
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

// ResultMarker satisfies [Result] when a result struct embeds it.
type ResultMarker struct{}

func (ResultMarker) isClispecResult() {}

// ResultSink accepts output from a work function. WriteJSON accepts an
// encoded [json.RawMessage] rather than any.
//
// [CLISink] implements trace correlation for these methods. Its JSON
// envelope stores the payload under result and metadata under _meta. Its
// text output adds a header on the first write when metadata exists,
// unless the body already starts with #meta. The header includes populated
// trace_id, span_id, and request_id fields.
type ResultSink interface {
	WriteJSON(ctx context.Context, payload json.RawMessage) error
	WriteText(ctx context.Context, body string) error
}

// WriteJSONValue encodes a typed result as JSON and writes it to sink.
//
// When sink collects read targets and value implements [ReadResult], the
// function collects the returned identities before writing the JSON. It
// returns an error if encoding, target collection, or writing fails.
//
//   - If AuditReadTargets returns an error, the function does not call
//     sink.WriteJSON.
//   - If sink does not collect read targets, the function skips
//     AuditReadTargets even when value implements ReadResult.
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

// CLISink writes to the factory output stream in the operator's chosen
// format.
type CLISink struct {
	f     *cli.Factory
	wrote bool
}

// NewCLISink returns a sink that writes to f.Out.
//
// The sink tracks whether output has begun to limit the text header to
// the first write. It reads the output format from f when rendering output.
//
//   - With only request_id=req-1 in ctx, the first text write adds
//     #meta request_id=req-1 before the body unless the body already
//     starts with #meta.
//   - A second text write prints its body without adding another header.
func NewCLISink(f *cli.Factory) *CLISink {
	return &CLISink{f: f, wrote: false}
}

// WriteJSON renders an encoded JSON payload in the selected output format.
//
// JSON mode wraps the payload in a metadata envelope. Text mode indents
// the JSON and uses the text header rules of CLISink.
//
// For the payload {"count":2}, text mode prints the following JSON when
// ctx does not supply correlation metadata:
//
//	{
//	  "count": 2
//	}
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

// WriteText renders text in the selected output format.
//
// JSON mode encodes body as the envelope's result string. Text mode uses
// the text header rules of CLISink.
//
// For example, a caller can write a status line:
//
//	return sink.WriteText(ctx, "Ready.")
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

// writeText applies [response.Text] on the first write to add a header
// when metadata exists and body does not already start with #meta.
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

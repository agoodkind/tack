package clispec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// ResultSink is how a work function returns output. Emit takes a structured
// value; Text takes a human line. Both carry the run's trace correlation:
// json output wraps the payload under result with a _meta block, text output
// prepends one header line with trace_id, span_id, and request_id.
type ResultSink interface {
	Emit(ctx context.Context, value any) error
	Text(ctx context.Context, body string) error
}

// CLISink writes to the factory's out stream in the operator's chosen format.
type CLISink struct {
	f     *cli.Factory
	wrote bool
}

// NewCLISink builds a sink that writes through f.
func NewCLISink(f *cli.Factory) *CLISink {
	return &CLISink{f: f}
}

// Emit renders a structured value. In json mode it is the envelope payload; in
// text mode it is indented JSON under the trace header.
func (s *CLISink) Emit(ctx context.Context, value any) error {
	if s.f.OutputFormat() == cli.FormatJSON {
		body, err := response.MarshalJSON(ctx, value)
		if err != nil {
			return err
		}
		return s.writeRaw(body)
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("clispec: encode result: %w", err)
	}
	return s.writeText(ctx, string(payload))
}

// Text renders a human line. In json mode the line becomes the envelope's
// result string so json output stays valid JSON.
func (s *CLISink) Text(ctx context.Context, body string) error {
	if s.f.OutputFormat() == cli.FormatJSON {
		out, err := response.MarshalJSON(ctx, body)
		if err != nil {
			return err
		}
		return s.writeRaw(out)
	}
	return s.writeText(ctx, body)
}

// writeText stamps the trace header on the first write only, so a command that
// writes more than once does not repeat the header.
func (s *CLISink) writeText(ctx context.Context, body string) error {
	out := body
	if !s.wrote {
		out = response.Text(ctx, body)
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return s.writeRaw([]byte(out))
}

func (s *CLISink) writeRaw(b []byte) error {
	s.wrote = true
	if _, err := s.f.Out.Write(b); err != nil {
		return fmt.Errorf("clispec: write output: %w", err)
	}
	return nil
}

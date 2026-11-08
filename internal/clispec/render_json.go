package clispec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// RenderJSON renders the JSON envelope in JSON mode or the indented payload
// in text mode. CLISink adds the metadata header when writing text. Callers
// can inspect rendered fields before CLISink writes the same representation.
func RenderJSON(ctx context.Context, format string, payload json.RawMessage) ([]byte, error) {
	if format == cli.FormatJSON {
		body, err := response.Marshal(ctx, payload)
		if err != nil {
			slog.ErrorContext(ctx, "clispec.envelope_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("clispec: envelope: %w", err)
		}
		return body, nil
	}
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, payload, "", "  "); err != nil {
		slog.ErrorContext(ctx, "clispec.indent_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("clispec: indent: %w", err)
	}
	return buffer.Bytes(), nil
}

package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

// printedDatagenDocument preserves the raw result bytes in either output
// format. It only removes the text metadata header before decoding JSON.
func printedDatagenDocument(t *testing.T, output []byte, format string) response.JSONDocument {
	t.Helper()
	var document response.JSONDocument
	if format == cli.FormatJSON {
		if err := json.Unmarshal(output, &document); err != nil {
			t.Fatalf("decode the printed JSON envelope: %v", err)
		}
		return document
	}
	header, body, found := bytes.Cut(output, []byte("\n"))
	if !found || !bytes.HasPrefix(header, []byte("#meta ")) || !json.Valid(body) {
		t.Fatal("the text output did not contain its metadata header and JSON result")
	}
	document.Result = body
	for _, field := range strings.Fields(string(header)) {
		key, value, _ := strings.Cut(field, "=")
		switch key {
		case "request_id":
			document.Meta.RequestID = value
		case "trace_id":
			document.Meta.TraceID = value
		case "span_id":
			document.Meta.SpanID = value
		}
	}
	return document
}

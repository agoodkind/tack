package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"goodkind.io/tack/internal/adapters/mcp/tools"
)

// readMCPRequestMetadata reads the JSON-RPC id and the session id off the
// request and rewinds the body so the MCP server can read it again.
func readMCPRequestMetadata(r *http.Request) (tools.MCPRequestMetadata, error) {
	metadata := tools.MCPRequestMetadata{RequestID: "", SessionID: r.Header.Get("Mcp-Session-Id")}
	if r.Body == nil {
		return metadata, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.ErrorContext(r.Context(), "mcp.request_body_read_failed", slog.String("err", err.Error()))
		return metadata, fmt.Errorf("read mcp request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	metadata.RequestID = jsonRPCRequestID(body)
	return metadata, nil
}

func jsonRPCRequestID(body []byte) string {
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &request); err != nil || len(request.ID) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(request.ID, &value); err == nil {
		return value
	}
	return string(request.ID)
}

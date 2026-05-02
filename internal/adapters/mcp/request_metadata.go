package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"goodkind.io/tack/internal/adapters/mcp/tools"
)

func withMCPRequestMetadata(r *http.Request) (*http.Request, error) {
	if r.Body == nil {
		return r, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read mcp request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	metadata := tools.MCPRequestMetadata{
		RequestID: jsonRPCRequestID(body),
		SessionID: r.Header.Get("Mcp-Session-Id"),
	}
	if metadata.RequestID == "" && metadata.SessionID == "" {
		return r, nil
	}
	return r.WithContext(tools.WithMCPRequestMetadata(r.Context(), metadata)), nil
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

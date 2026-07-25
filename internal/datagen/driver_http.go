package datagen

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
)

func (d *Driver) send(
	ctx context.Context,
	token string,
	body []byte,
	includeSession bool,
) (*http.Response, error) {
	if d.graph == nil {
		return nil, fmt.Errorf("qa datagen: runtime graph is required")
	}
	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/mcp",
		bytes.NewReader(body),
	)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if includeSession {
		request.Header.Set("Mcp-Session-Id", d.sessionID)
	}
	recorder := httptest.NewRecorder()
	d.graph.AuthMiddleware(d.graph.MCPHandler).ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

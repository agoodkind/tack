//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

// callSnapshotSearch submits an authenticated MCP request to the harness's
// production HTTP route with a context that the test can cancel.
func callSnapshotSearch(ctx context.Context, fixture failureFixture, harness *integration.MCPHarness, cursor string) snapshotSearchResult {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": map[string]any{
			"workspace_reference": harness.Workspace, "query": failureAuditQuery, "cursor": cursor,
		}},
	})
	if err != nil {
		return snapshotSearchResult{err: fmt.Errorf("encode the interrupted search: %w", err)}
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+fixture.Workspaces[0].Actors[0].Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	integration.MCPHandler(harness).ServeHTTP(recorder, request)
	return snapshotSearchResult{text: recorder.Body.String(), status: recorder.Code, err: nil}
}

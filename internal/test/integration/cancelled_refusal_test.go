package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/telemetry"
)

func TestRuntimeAuditReadRefusalAfterCancellation(t *testing.T) {
	server := startPublicServer(t, nil)
	ledger := startPublicLedger(t, server)
	requestID := newRequestID(t, "cancelled-refusal")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	authenticate := func(next http.Handler) http.Handler {
		actual := server.graph.AuthMiddleware(next)

		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			actual.ServeHTTP(writer, request)
			cancel()
		})
	}
	handler := server.graph.Requests.MCP(authenticate, server.graph.MCPHandler)
	body := rpcBody(
		t,
		"tools/call",
		map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}},
	)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	telemetry.RequestLogger(handler).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("MCP status = %d, want 401", response.Code)
	}

	row := ledger.row(t, audit.SystemOrgID(), requestID)
	requireReadRow(
		t,
		row,
		audit.VerbAuthLoginFailed,
		audit.OutcomeRefused,
		auth.RejectionMissingAuthorization,
		nil,
	)
	if row.ActorID != uuid.Nil || row.Context.RequestID != requestID ||
		row.Context.Target == nil || row.Context.Target.URI != "/mcp" {
		t.Fatalf("authentication refusal actor = %s, context = %+v", row.ActorID, row.Context)
	}
}

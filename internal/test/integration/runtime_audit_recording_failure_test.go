package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/telemetry"
)

// TestRuntimeAuditCancelledProductRead checks the response after cancellation.
func TestRuntimeAuditCancelledProductRead(t *testing.T) {
	server := startPublicServer(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	authenticate := func(next http.Handler) http.Handler {
		return server.graph.AuthMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			cancel()
			next.ServeHTTP(writer, request)
		}))
	}
	handler := server.graph.Requests.MCP(authenticate, server.graph.MCPHandler)
	body := rpcBody(t, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, newRequestID(t, "admission-failure"))
	request.Header.Set("Authorization", "Bearer "+server.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	telemetry.RequestLogger(handler).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), `"result"`) {
		t.Fatalf("cancelled read returned a result: %s", response.Body.String())
	}
}

// rolePool opens a pool that runs every statement as role.
func rolePool(t *testing.T, dsn, role string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig failed for the ledger address: %v", err)
	}

	config.ConnConfig.RuntimeParams["role"] = role
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig failed for the %s pool: %v", role, err)
	}

	t.Cleanup(pool.Close)

	return pool
}

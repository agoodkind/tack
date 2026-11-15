package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// TestRuntimeAuditReadAttemptBeforeHandler checks the ledger from inside the handler.
func TestRuntimeAuditReadAttemptBeforeHandler(t *testing.T) {
	server := startPublicServer(t, nil)
	ledger := startPublicLedger(t, server)
	requestID := newRequestID(t, "attempt")
	handler := server.graph.Requests.Health(
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			row := ledger.row(t, audit.SystemOrgID(), requestID)
			requireReadRow(t, row, audit.VerbHTTPHealthRead, audit.OutcomeAttempted, "", nil)
			writer.WriteHeader(http.StatusServiceUnavailable)
		}),
	)
	response := serveRecorded(t.Context(), handler, requestID)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want 503", response.Code)
	}
}

// serveRecorded sends one GET request for the health check route with
// requestID through the request logger and handler.
func serveRecorded(
	ctx context.Context,
	handler http.Handler,
	requestID string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/healthz", nil)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	telemetry.RequestLogger(handler).ServeHTTP(response, request)

	return response
}

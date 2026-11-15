package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

func TestRuntimeAuditReadRefusalAfterCancellation(t *testing.T) {
	server := startPublicServer(t, nil)
	ledger := startPublicLedger(t, server)
	requestID := newRequestID(t, "cancelled-refusal")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := server.graph.Requests.Diagnostics(
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			attempt := ledger.row(t, audit.SystemOrgID(), requestID)
			requireReadRow(
				t,
				attempt,
				audit.VerbHTTPDiagnosticsRead,
				audit.OutcomeAttempted,
				"",
				nil,
			)
			writer.WriteHeader(http.StatusForbidden)
			cancel()
		}),
	)
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/debug/vars", nil)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	telemetry.RequestLogger(handler).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("diagnostics status = %d, want 403", response.Code)
	}

	rows := ledger.requestRows(
		t,
		audit.SystemOrgID(),
		requestID,
		func(rows []audit.Row) bool {
			return len(rows) == 2
		},
	)
	var attempt, refusal audit.Row

	for _, row := range rows {
		switch row.Outcome {
		case audit.OutcomeAttempted:
			attempt = row
		case audit.OutcomeRefused:
			refusal = row
		}
	}

	requireReadRow(t, attempt, audit.VerbHTTPDiagnosticsRead, audit.OutcomeAttempted, "", nil)
	requireReadRow(
		t,
		refusal,
		audit.VerbHTTPDiagnosticsRead,
		audit.OutcomeRefused,
		"forbidden",
		nil,
	)
	if attempt.Context.OperationID != refusal.Context.OperationID ||
		attempt.ActorID != refusal.ActorID || refusal.Context.Target == nil ||
		refusal.Context.Target.URI != "/debug/vars" {
		t.Fatalf("attempt context = %+v, refusal context = %+v", attempt.Context, refusal.Context)
	}
}

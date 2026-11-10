package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/testenv"
)

// withheldBody is the body that the stale owner prepares and must not release.
const withheldBody = "stale owner body"

// TestRuntimeAuditReadFinalizeAfterCancel cancels the request context inside
// the health check handler before the handler writes a response. The request
// recorder must save a terminal failed event with the request_canceled code
// after the cancellation, without read recovery.
func TestRuntimeAuditReadFinalizeAfterCancel(t *testing.T) {
	server := startPublicServer(t, nil)
	recorder := mcpadapter.NewRequestRecorder(server.graph.ProductReads)
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	handler := recorder.Health(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		cancel()
		<-request.Context().Done()
	}))
	requestID := newRequestID(t, "canceled")
	response := serveRecorded(ctx, handler, requestID)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("canceled health check response = %d %q, want 200 with an empty body", response.Code, response.Body.String())
	}
	ledger := startPublicLedger(t, server)
	row := ledger.row(t, audit.SystemOrgID(), requestID)
	requireReadRow(t, row, audit.VerbHTTPHealthRead, audit.OutcomeFailed, "request_canceled", response.Body.Bytes())
}

// TestRuntimeAuditReadStaleOwnerWithheld runs read recovery for the pending
// read of a health check while its handler runs. Recovery replaces the read
// owner before the request recorder finalizes the read. The recorder must
// answer with HTTP 503 without the prepared body, and the outbox must contain
// one incomplete event for the read.
func TestRuntimeAuditReadStaleOwnerWithheld(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New failed for the administrator pool: %v", err)
	}
	t.Cleanup(admin.Close)
	store := audit.NewPoolReadLifecycle(applicationRolePool(t, dsn))
	recorder := mcpadapter.NewRequestRecorder(store)
	requestID := newRequestID(t, "stale-owner")
	eventID := uuid.Nil
	handler := recorder.Health(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rows, err := admin.Query(request.Context(), readTestSQL(t, "runtime_audit_public_expire_request.sql"), requestID)
		if err != nil {
			t.Fatalf("expiring the pending read for request %s failed: %v", requestID, err)
		}
		eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(eventIDs) != 1 {
			t.Fatalf("pending read IDs for request %s = %v, error = %v, want one ID", requestID, eventIDs, err)
		}
		eventID = eventIDs[0]
		recovered, err := store.RecoverExpired(request.Context(), audit.ReadBucket(eventID), audit.ReadRecoveryScanLimit)
		if err != nil || recovered < 1 {
			t.Fatalf("recovery of read %s = %d, error = %v, want at least 1 recovery", eventID, recovered, err)
		}
		_, _ = writer.Write([]byte(withheldBody))
	}))
	response := serveRecorded(context.WithoutCancel(t.Context()), handler, requestID)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), withheldBody) {
		t.Fatalf("stale owner response = %d %q, want 503 without the prepared body", response.Code, response.Body.String())
	}
	event := requireTerminal(t, yugabyteReadBackend(t), eventID, audit.OutcomeIncomplete)
	if event.Context.RequestID != requestID {
		t.Fatalf("recovered event %s request = %q, want %q", eventID, event.Context.RequestID, requestID)
	}
}

// serveRecorded sends one GET request for the health check route with
// requestID through the request logger and handler.
func serveRecorded(ctx context.Context, handler http.Handler, requestID string) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/healthz", nil)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	telemetry.RequestLogger(handler).ServeHTTP(response, request)
	return response
}

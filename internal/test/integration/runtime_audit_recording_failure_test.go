package integration_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const finalizeDeniedRole = "tack_test_read_finalize_denied"

// TestRuntimeAuditReadRecordingFailure checks that the server returns 503 instead of a read result
// when the store refuses to save the final event after saving the admission.
// It checks that recovery saves exactly one incomplete event for the read.
func TestRuntimeAuditReadRecordingFailure(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New failed for the administrator pool: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), readTestSQL(t, "runtime_audit_public_finalize_denied.sql")); err != nil {
		t.Fatalf("admin.Exec failed while creating role %s: %v", finalizeDeniedRole, err)
	}
	denied := audit.NewPoolReadLifecycle(rolePool(t, dsn, finalizeDeniedRole))
	server := startPublicServer(t, func(*appruntime.Graph) audit.ReadLifecycle { return denied })
	requestID := newRequestID(t, "recording-failure")
	response := server.rpc(t, requestID, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	if response.status != http.StatusServiceUnavailable || strings.Contains(string(response.body), server.workspace) {
		t.Fatalf("response = %d %s, want 503 without workspace %s", response.status, response.body, server.workspace)
	}
	rows, err := admin.Query(t.Context(), readTestSQL(t, "runtime_audit_public_expire_request.sql"), requestID)
	if err != nil {
		t.Fatalf("expiring the pending read for request %s failed: %v", requestID, err)
	}
	eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(eventIDs) != 1 {
		t.Fatalf("pending read IDs for request %s = %v, error = %v, want one ID and a nil error", requestID, eventIDs, err)
	}
	eventID := eventIDs[0]
	recovery := audit.NewPoolReadLifecycle(applicationRolePool(t, dsn))
	recovered, err := recovery.RecoverExpired(t.Context(), audit.ReadBucket(eventID), audit.ReadRecoveryScanLimit)
	if err != nil || recovered < 1 {
		t.Fatalf("read %s recovery count = %d, error = %v, want at least 1 recovery and a nil error", eventID, recovered, err)
	}
	event := requireTerminal(t, yugabyteReadBackend(t), eventID, audit.OutcomeIncomplete)
	if event.Actor.ID != server.userID || event.Context.RequestID != requestID {
		t.Fatalf("recovered event %s actor = %s, request = %q, want %s %q",
			eventID, event.Actor.ID, event.Context.RequestID, server.userID, requestID)
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

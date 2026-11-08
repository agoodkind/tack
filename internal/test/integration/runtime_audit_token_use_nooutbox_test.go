package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestRuntimeAuditTokenUseWithoutOutbox authenticates with a real API token
// through auth.Bearer on a server that uses only the YugabyteDB writer as its
// audit backend, without Kafka brokers or an outbox. The ledger must contain
// the auth.token_used row for the request when the response returns.
func TestRuntimeAuditTokenUseWithoutOutbox(t *testing.T) {
	t.Setenv("ENV", "production")
	cfg := publicConfig(t)
	ledgerDSN := testenv.Ledger(t)
	cfg.AuditKafkaBrokers, cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", ledgerDSN, false
	cfg.AuditReadFlushInterval = tokenUseNeverFlushInterval
	server := buildPublicServer(t, cfg, nil)
	reader, err := audit.NewReader(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("audit.NewReader failed: %v", err)
	}
	t.Cleanup(reader.Close)

	requestID := newRequestID(t, "token-use-direct")
	response := server.rpc(t, requestID, "tools/list", map[string]any{})
	if response.status != 200 {
		t.Fatalf("tools/list status = %d, want 200: %s", response.status, response.body)
	}
	rows := tokenUsedRows(t, reader, server.orgID, requestID)
	if len(rows) != 1 {
		t.Fatalf("ledger has %d auth.token_used rows for request %s when the response returned, want 1", len(rows), requestID)
	}
	requireTokenUsedRowFields(t, rows[0], server.userID, fixtureTokenID(t, server), requestID)
}

package integration_test

import (
	_ "embed"
	"net/http"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// auditUnavailableResponse is the body of the HTTP 503 response for a refused audit event.
const auditUnavailableResponse = `{"error":"audit recording unavailable"}`

var (
	// tokenUseBareLoginTemplate creates a login without a base role.
	//go:embed testdata/token_use_bare_login.sql
	tokenUseBareLoginTemplate string
	// tokenUseWriterLoginTemplate creates a login with the audit_writer base
	// role, the grant that seed-roles gives the audit writer login.
	//go:embed testdata/token_use_writer_login.sql
	tokenUseWriterLoginTemplate string
	// tokenUseLoginDropTemplate drops either login.
	//go:embed testdata/token_use_bare_login_drop.sql
	tokenUseLoginDropTemplate string
)

// TestRuntimeAuditTokenUseRefusedWhenRecordingFails authenticates with a real
// API token on a server that uses only the YugabyteDB writer as its audit
// backend. The writer logs in with a role that lacks an audit role, and the
// ledger refuses its insert. The request must receive HTTP 503 with the audit
// unavailable body, and the ledger must not contain an auth.token_used row for
// the request. A second server then uses a writer login with the audit_writer
// role. Its request must succeed, and the ledger must contain its row with the
// token owner as actor, the token target, the request ID, and the completed
// outcome.
func TestRuntimeAuditTokenUseRefusedWhenRecordingFails(t *testing.T) {
	t.Setenv("ENV", "production")
	cfg := publicConfig(t)
	ledgerDSN := testenv.Ledger(t)
	admin := ledgerAdminPool(t)
	cfg.AuditKafkaBrokers, cfg.AuditAllowUnrecorded = "", false
	cfg.AuditWriterDSN = templateLoginDSN(t, admin, tokenUseBareLoginTemplate, tokenUseLoginDropTemplate)
	refusing := buildPublicServer(t, cfg, nil)
	reader, err := audit.NewReader(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("audit.NewReader failed: %v", err)
	}
	t.Cleanup(reader.Close)

	refusedID := newRequestID(t, "token-use-refused")
	refused := refusing.rpc(t, refusedID, "tools/list", map[string]any{})
	if refused.status != http.StatusServiceUnavailable || string(refused.body) != auditUnavailableResponse {
		t.Fatalf("response = %d %s, want 503 %s", refused.status, refused.body, auditUnavailableResponse)
	}
	requireNoLedgerTokenUsed(t, reader, refusing.orgID, refusedID)

	cfg.AuditWriterDSN = templateLoginDSN(t, admin, tokenUseWriterLoginTemplate, tokenUseLoginDropTemplate)
	recovered := buildPublicServer(t, cfg, nil)
	recoveredID := newRequestID(t, "token-use-recovered")
	response := recovered.rpc(t, recoveredID, "tools/list", map[string]any{})
	if response.status != http.StatusOK {
		t.Fatalf("tools/list status with the writer login = %d, want 200: %s", response.status, response.body)
	}
	rows := tokenUsedRows(t, reader, recovered.orgID, recoveredID)
	if len(rows) != 1 {
		t.Fatalf("ledger has %d auth.token_used rows for request %s, want 1", len(rows), recoveredID)
	}
	requireTokenUsedRowFields(t, rows[0], recovered.userID, fixtureTokenID(t, recovered), recoveredID)
}

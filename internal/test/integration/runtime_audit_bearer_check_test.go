package integration_test

import (
	"errors"
	"net/http"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// authenticationUnavailableResponse is the HTTP 503 response body for a
	// failed token check.
	authenticationUnavailableResponse = `{"error":"authentication unavailable"}`
	// hideTokensStatement renames the token table. Token reads then fail with a
	// database error.
	hideTokensStatement = "ALTER TABLE public.api_tokens RENAME TO api_tokens_check_test"
	// restoreTokensStatement restores the token table name.
	restoreTokensStatement = "ALTER TABLE public.api_tokens_check_test RENAME TO api_tokens"
)

// TestRuntimeAuditBearerCheckFailure builds a public server that does not
// cache tokens and renames the token table. The token check then fails with a
// database error. The test requires HTTP 503 with the authentication
// unavailable body. It also requires one failed auth.token_rejected event in
// the system organization's ledger with the bearer_check_failed code.
func TestRuntimeAuditBearerCheckFailure(t *testing.T) {
	t.Setenv("ENV", "production")
	cfg := publicConfig(t)
	cfg.AuthTokenCacheLifetime = 0
	server := newPublicServer(t, cfg, nil)
	restoreTokens := hideLedgerTable(t, hideTokensStatement, restoreTokensStatement)
	var requestID string
	var response publicResponse
	integration.ClusterEventually(t, "fail the token check after the rename", func() error {
		requestID = newRequestID(t, "bearer-check-failure")
		response = server.rpc(t, requestID, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
		if response.status == http.StatusOK {
			return errors.New("the token check still returned HTTP 200")
		}
		return nil
	})
	restoreTokens()
	if response.status != http.StatusServiceUnavailable || string(response.body) != authenticationUnavailableResponse {
		t.Fatalf("response = %d %s, want 503 %s", response.status, response.body, authenticationUnavailableResponse)
	}

	ledger := startPublicLedger(t, server)
	row := ledger.row(t, audit.SystemOrgID(), requestID)
	requireReadRow(t, row, audit.VerbAuthTokenRejected, audit.OutcomeFailed, auth.RejectionBearerCheckFailed, response.body)
}

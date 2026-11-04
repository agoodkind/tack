package integration_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

const (
	// organizationUnavailableResponse is the body of the HTTP 503 response for
	// a failed organization lookup.
	organizationUnavailableResponse = `{"error":"organization unavailable"}`
	// hideMembershipsStatement renames the membership table. Membership reads
	// then fail with a database error.
	hideMembershipsStatement = "ALTER TABLE public.org_members RENAME TO org_members_lookup_test"
	// restoreMembershipsStatement restores the membership table name.
	restoreMembershipsStatement = "ALTER TABLE public.org_members_lookup_test RENAME TO org_members"
)

// hideLedgerTable runs the hide statement on the ledger.
// It returns a function that runs the restore statement.
// If the test ends before that function runs, cleanup runs the restore
// statement.
func hideLedgerTable(t *testing.T, hide, restore string) func() {
	t.Helper()
	admin, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("pgxpool.New failed for the administrator pool: %v", err)
	}
	t.Cleanup(admin.Close)
	hidden := false
	t.Cleanup(func() {
		if !hidden {
			return
		}
		if _, err := admin.Exec(context.WithoutCancel(t.Context()), restore); err != nil {
			t.Errorf("run %q: %v", restore, err)
		}
	})
	if _, err := admin.Exec(t.Context(), hide); err != nil {
		t.Fatalf("run %q: %v", hide, err)
	}
	hidden = true
	return func() {
		t.Helper()
		if _, err := admin.Exec(t.Context(), restore); err != nil {
			t.Fatalf("run %q: %v", restore, err)
		}
		hidden = false
	}
}

// TestRuntimeAuditOrganizationLookupFailure builds a public server that does
// not cache memberships. It sends one ping, which stores the caller's token in
// the token cache. It then renames the membership table and calls
// tack_list_workspaces, and the organization lookup fails with a database
// error. The server must answer HTTP 503 with the organization unavailable
// body. The ledger must contain one failed workspace.list event in the system
// organization for the verified user with the organization_unavailable code.
func TestRuntimeAuditOrganizationLookupFailure(t *testing.T) {
	cfg := publicConfig(t)
	cfg.AuthMembershipCacheLifetime = 0
	server := newPublicServer(t, cfg, nil)
	if warm := server.rpc(t, newRequestID(t, "lookup-warm"), "ping", map[string]any{}); warm.status != http.StatusOK {
		t.Fatalf("ping status = %d, want 200: %s", warm.status, warm.body)
	}
	restoreMemberships := hideLedgerTable(t, hideMembershipsStatement, restoreMembershipsStatement)
	requestID := newRequestID(t, "lookup-failure")
	response := server.rpc(t, requestID, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	restoreMemberships()
	if response.status != http.StatusServiceUnavailable || string(response.body) != organizationUnavailableResponse {
		t.Fatalf("response = %d %s, want 503 %s", response.status, response.body, organizationUnavailableResponse)
	}

	ledger := startPublicLedger(t, server)
	row := ledger.row(t, audit.SystemOrgID(), requestID)
	requireReadRow(t, row, audit.VerbWorkspaceList, audit.OutcomeFailed, "organization_unavailable", response.body)
	if row.ActorID != server.userID || row.Context.Tool != "tack_list_workspaces" {
		t.Fatalf("event %s actor = %s, tool = %q, want the verified user %s and tack_list_workspaces",
			row.EventID, row.ActorID, row.Context.Tool, server.userID)
	}
}

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
		if _, err := admin.Exec(context.WithoutCancel(t.Context()), restoreMembershipsStatement); err != nil {
			t.Errorf("restore the membership table: %v", err)
		}
	})
	if _, err := admin.Exec(t.Context(), hideMembershipsStatement); err != nil {
		t.Fatalf("rename the membership table: %v", err)
	}
	hidden = true
	requestID := newRequestID(t, "lookup-failure")
	response := server.rpc(t, requestID, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	if _, err := admin.Exec(t.Context(), restoreMembershipsStatement); err != nil {
		t.Fatalf("restore the membership table: %v", err)
	}
	hidden = false
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

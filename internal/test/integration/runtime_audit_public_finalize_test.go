package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

func TestRuntimeAuditProductReadAttempt(t *testing.T) {
	server := startPublicServer(t, nil)
	ledger := startPublicLedger(t, server)
	requestID := newRequestID(t, "attempt")
	response := server.rpc(
		t,
		requestID,
		"tools/call",
		map[string]any{
			"name":      "tack_list_workspaces",
			"arguments": map[string]any{},
		},
	)
	toolText(t, response)
	row := ledger.row(t, server.orgID, requestID)
	requireReadRow(t, row, audit.VerbWorkspaceList, audit.OutcomeAttempted, "", response.body)
	if row.ActorID != server.userID || row.Context.RequestID != requestID ||
		row.Context.Target == nil || row.Context.Target.URI != "/mcp" {
		t.Fatalf("read attempt actor = %s, context = %+v", row.ActorID, row.Context)
	}
}

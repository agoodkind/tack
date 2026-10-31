package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// managedRefusalReason is part of the error that the node service returns
// for a request that sets a service-managed property.
const managedRefusalReason = "a request cannot set them"

// TestRuntimeAuditManagedPropertyRefusals creates a project and an issue
// through the public MCP boundary with the real recorder, relay, consumer,
// and ledger. It then calls tack_create_issue with sequence and
// tack_update_issue with scope_id in the properties. The server must refuse
// both calls. For each call, the ledger must record the auth.token_used event
// and exactly one other event under the request operation: a refused result
// for the verified user with the invalid_argument code, a message that names
// the property, and no delta. The refused create must target the MCP route of
// tack_create_issue, and the refused update must target the issue. The ledger
// must not record a change event for either request.
func TestRuntimeAuditManagedPropertyRefusals(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "MNG" + strings.ToUpper(uuid.NewString()[:5])
	projectID := server.createNode(t, "tack_create_project", map[string]any{
		"workspace_reference": server.workspace, "name": identifier,
		"properties": map[string]any{"identifier": identifier},
	})
	inProject := map[string]any{"workspace_reference": server.workspace, "project_reference": identifier}
	issueID := server.createNode(t, "tack_create_issue", withName(inProject, "Managed refusal issue"))

	createID, updateID := newRequestID(t, "managed-create"), newRequestID(t, "managed-update")
	created := server.rpc(t, createID, "tools/call", map[string]any{"name": "tack_create_issue", "arguments": map[string]any{
		"workspace_reference": server.workspace, "project_reference": identifier, "name": "Managed sequence issue",
		"properties": map[string]any{"sequence": 999},
	}})
	requireToolRefusal(t, created, managedRefusalReason)
	updated := server.rpc(t, updateID, "tools/call", map[string]any{"name": "tack_update_issue", "arguments": map[string]any{
		"workspace_reference": server.workspace, "node_id": issueID.String(),
		"properties": map[string]any{"scope_id": projectID.String()},
	}})
	requireToolRefusal(t, updated, managedRefusalReason)

	createRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, createID, hasAuthAndOther), server.userID)
	requireManagedRefusalRow(t, createRow, audit.VerbNodeCreate, "properties.sequence", server.userID)
	createTarget := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "tack_create_issue", URI: "/mcp",
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	if *createRow.Context.Target != createTarget {
		t.Fatalf("refused create event %s target = %+v, want the tool route %+v", createRow.EventID, createRow.Context.Target, createTarget)
	}
	updateRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, updateID, hasAuthAndOther), server.userID)
	requireManagedRefusalRow(t, updateRow, audit.VerbNodeUpdate, "properties.scope_id", server.userID)
	if target := updateRow.Context.Target; target.Kind != audit.TargetKindNode || target.NodeType != "issue" || target.ID != issueID {
		t.Fatalf("refused update event %s target = %+v, want issue %s", updateRow.EventID, target, issueID)
	}
}

// requireManagedRefusalRow requires a refused result event of verb for
// actorID with the invalid_argument code, a message that contains property
// and the refusal reason, a target, and no delta.
func requireManagedRefusalRow(t *testing.T, row audit.Row, verb audit.Verb, property string, actorID uuid.UUID) {
	t.Helper()
	requireResultRow(t, row, verb, audit.OutcomeRefused, "invalid_argument")
	if row.ActorID != actorID || !strings.Contains(row.Error.Message, property) || !strings.Contains(row.Error.Message, managedRefusalReason) {
		t.Fatalf("event %s actor = %s, error message = %q; want actor %s and a message with %q and %q",
			row.EventID, row.ActorID, row.Error.Message, actorID, property, managedRefusalReason)
	}
	if row.Context.Target == nil || row.Delta != nil {
		t.Fatalf("event %s target = %+v, delta = %+v; want a target and no delta", row.EventID, row.Context.Target, row.Delta)
	}
}

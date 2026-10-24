package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	// reservedParentReason is part of the error that tack_add_relationship
	// returns for the child_of relation type.
	reservedParentReason = "the child_of relation type is reserved for the parent that parent_id sets"
	// onlyParentReason is part of the error that tack_remove_relationship
	// returns for the only hierarchy parent edge of a node.
	onlyParentReason = "is its only hierarchy parent"
)

// TestRuntimeAuditRefusedRelationshipChanges creates a project, an epic, and
// an issue under the project through the public MCP boundary. It calls
// tack_add_relationship with child_of from the issue to the epic, and
// tack_remove_relationship on the child_of edge from the issue to the
// project. The server must refuse both calls and leave the child_of edges of
// the issue unchanged. For each call, the ledger must record one refused
// result for the verified user with the relationship target, the typed code,
// and the refusal reason.
func TestRuntimeAuditRefusedRelationshipChanges(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "AUD" + strings.ToUpper(uuid.NewString()[:5])
	projectID := server.createNode(t, "tack_create_project", map[string]any{
		"workspace_reference": server.workspace, "name": identifier,
		"properties": map[string]any{"identifier": identifier},
	})
	inProject := map[string]any{"workspace_reference": server.workspace, "project_reference": identifier}
	epicID := server.createNode(t, "tack_create_epic", withName(inProject, "Refusal epic"))
	issueID := server.createNode(t, "tack_create_issue", withName(inProject, "Refusal issue"))
	parentsBefore := server.childOfEdges(t, issueID)

	addID, removeID := newRequestID(t, "refused-add"), newRequestID(t, "refused-remove")
	added := server.rpc(t, addID, "tools/call", map[string]any{"name": "tack_add_relationship", "arguments": map[string]any{
		"source_id": issueID.String(), "relation_type": "child_of", "target_id": epicID.String(),
	}})
	requireToolRefusal(t, added, reservedParentReason)
	removed := server.rpc(t, removeID, "tools/call", map[string]any{"name": "tack_remove_relationship", "arguments": map[string]any{
		"source_id": issueID.String(), "relation_type": "child_of", "target_id": projectID.String(),
	}})
	requireToolRefusal(t, removed, onlyParentReason)
	if parentsAfter := server.childOfEdges(t, issueID); parentsAfter != parentsBefore {
		t.Fatalf("child_of edges of issue %s changed after the refused calls:\nbefore:\n%s\nafter:\n%s", issueID, parentsBefore, parentsAfter)
	}

	addRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, addID, hasAuthAndOther), server.userID)
	requireRefusedRelationshipRow(t, addRow, audit.VerbRelationshipAdd, "invalid_argument", reservedParentReason, server.userID,
		audit.Target{
			Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
			RelationType: "child_of", ID: uuid.Nil, SourceID: issueID, TargetID: epicID,
		})
	removeRow := onlyResultRow(t, ledger.requestRows(t, server.orgID, removeID, hasAuthAndOther), server.userID)
	requireRefusedRelationshipRow(t, removeRow, audit.VerbRelationshipRemove, "failed_precondition", onlyParentReason, server.userID,
		audit.Target{
			Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
			RelationType: "child_of", ID: uuid.Nil, SourceID: issueID, TargetID: projectID,
		})
}

// requireRefusedRelationshipRow requires a refused result event of verb for
// actorID with code, a message that contains reason, the relationship
// target, and no delta.
func requireRefusedRelationshipRow(t *testing.T, row audit.Row, verb audit.Verb, code, reason string, actorID uuid.UUID, target audit.Target) {
	t.Helper()
	requireResultRow(t, row, verb, audit.OutcomeRefused, code)
	if row.ActorID != actorID || !strings.Contains(row.Error.Message, reason) {
		t.Fatalf("event %s actor = %s, error message = %q; want actor %s and a message with %q", row.EventID, row.ActorID, row.Error.Message, actorID, reason)
	}
	if row.Context.Target == nil || *row.Context.Target != target || row.Delta != nil {
		t.Fatalf("event %s target = %+v, delta = %+v; want target %+v and no delta", row.EventID, row.Context.Target, row.Delta, target)
	}
}

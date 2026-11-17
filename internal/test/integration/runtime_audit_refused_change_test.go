package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

const (
	// selfParentReason identifies the refusal of a node as its own parent.
	selfParentReason = "is the node, one of its descendants, or in a child_of cycle"
	// onlyParentReason is part of the error that tack_remove_relationship
	// returns for the only hierarchy parent edge of a node.
	onlyParentReason = "is its only hierarchy parent"
)

// TestRuntimeAuditRefusedRelationshipChanges creates a project and
// an issue under the project through the public MCP boundary. It calls
// tack_add_relationship with child_of from the issue to itself, and
// tack_remove_relationship on the child_of edge from the issue to the
// project. The server must refuse both calls and leave the child_of edges of
// the issue unchanged. Each call must record authentication and an attempt.
func TestRuntimeAuditRefusedRelationshipChanges(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "AUD" + strings.ToUpper(uuid.NewString()[:5])
	projectID := server.createNode(
		t,
		"tack_create_project",
		map[string]any{
			"workspace_reference": server.workspace,
			"name":                identifier,
			"properties":          map[string]any{"identifier": identifier},
		},
	)
	inProject := map[string]any{
		"workspace_reference": server.workspace,
		"project_reference":   identifier,
	}
	issueID := server.createNode(t, "tack_create_issue", withName(inProject, "Refusal issue"))
	parentsBefore := server.childOfEdges(t, issueID)

	addID, removeID := newRequestID(t, "refused-add"), newRequestID(t, "refused-remove")
	added := server.rpc(
		t,
		addID,
		"tools/call",
		map[string]any{
			"name": "tack_add_relationship",
			"arguments": map[string]any{
				"source_id":     issueID.String(),
				"relation_type": "child_of",
				"target_id":     issueID.String(),
			},
		},
	)
	requireToolRefusal(t, added, selfParentReason)
	removed := server.rpc(
		t,
		removeID,
		"tools/call",
		map[string]any{
			"name": "tack_remove_relationship",
			"arguments": map[string]any{
				"source_id":     issueID.String(),
				"relation_type": "child_of",
				"target_id":     projectID.String(),
			},
		},
	)
	requireToolRefusal(t, removed, onlyParentReason)
	if parentsAfter := server.childOfEdges(t, issueID); parentsAfter != parentsBefore {
		t.Fatalf(
			"child_of edges of issue %s changed after the refused calls:\nbefore:\n%s\nafter:\n%s",
			issueID,
			parentsBefore,
			parentsAfter,
		)
	}

	addTarget := &audit.Target{
		Kind:         audit.TargetKindRelationship,
		SourceID:     issueID,
		TargetID:     issueID,
		RelationType: "child_of",
	}
	removeTarget := &audit.Target{
		Kind:         audit.TargetKindRelationship,
		SourceID:     issueID,
		TargetID:     projectID,
		RelationType: "child_of",
	}
	requireMutationRequest(
		t,
		ledger,
		server,
		addID,
		audit.VerbRelationshipAdd,
		"tack_add_relationship",
		"",
		addTarget,
	)
	requireMutationRequest(
		t,
		ledger,
		server,
		removeID,
		audit.VerbRelationshipRemove,
		"tack_remove_relationship",
		"",
		removeTarget,
	)
}

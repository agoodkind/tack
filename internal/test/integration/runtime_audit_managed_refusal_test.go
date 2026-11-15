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
// both calls and identify the rejected properties. Each call must record
// authentication and a mutation attempt.
func TestRuntimeAuditManagedPropertyRefusals(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "MNG" + strings.ToUpper(uuid.NewString()[:5])
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
	issueID := server.createNode(
		t,
		"tack_create_issue",
		withName(inProject, "Managed refusal issue"),
	)

	createID, updateID := newRequestID(t, "managed-create"), newRequestID(t, "managed-update")
	created := server.rpc(
		t,
		createID,
		"tools/call",
		map[string]any{
			"name": "tack_create_issue",
			"arguments": map[string]any{
				"workspace_reference": server.workspace,
				"project_reference":   identifier,
				"name":                "Managed sequence issue",
				"properties":          map[string]any{"sequence": 999},
			},
		},
	)
	requireToolRefusal(t, created, managedRefusalReason)
	updated := server.rpc(
		t,
		updateID,
		"tools/call",
		map[string]any{
			"name": "tack_update_issue",
			"arguments": map[string]any{
				"workspace_reference": server.workspace,
				"node_id":             issueID.String(),
				"properties":          map[string]any{"scope_id": projectID.String()},
			},
		},
	)
	requireToolRefusal(t, updated, managedRefusalReason)
	if !strings.Contains(string(created.body), "properties.sequence") ||
		!strings.Contains(string(updated.body), "properties.scope_id") {
		t.Fatalf("managed property responses = %s, %s", created.body, updated.body)
	}

	requireMutationRequest(
		t,
		ledger,
		server,
		createID,
		audit.VerbNodeCreate,
		"tack_create_issue",
		"",
		nil,
	)
	requireMutationRequest(
		t,
		ledger,
		server,
		updateID,
		audit.VerbNodeUpdate,
		"tack_update_issue",
		issueID.String(),
		nil,
	)
}

package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// memberRole is the server actor's role in the second organization.
	memberRole = 20
	// harnessTransactionTimeout limits the duration of each FoundationDB transaction for identity lookup.
	harnessTransactionTimeout = 5 * time.Second
)

// nodeListQuery returns the query for every node of nodeType in orgID.
func nodeListQuery(orgID uuid.UUID, nodeType string) node.NodeListQuery {
	return node.NodeListQuery{
		OrgID: orgID, NodeType: nodeType, ByProperty: nil, BySourceRelation: nil, ByTargetRelation: nil,
		CreatedAfter: nil, CreatedBefore: nil, PropFilters: nil, Limit: 0, Cursor: "",
	}
}

func TestRuntimeAuditMultiOrganizationRead(t *testing.T) {
	server := startPublicServer(t, nil)
	secondOrg := addSecondOrganization(t, server)
	organizations := []uuid.UUID{server.orgID, secondOrg}
	workspacesID := newRequestID(t, "multi-workspaces")
	workspaces := server.rpc(t, workspacesID, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	toolsID := newRequestID(t, "multi-tools")
	toolList := server.rpc(t, toolsID, "tools/list", map[string]any{})
	ledger := startPublicLedger(t, server)
	owners := identityOwners(t, server, organizations)
	cases := []struct {
		requestID string
		body      []byte
		verb      audit.Verb
	}{
		{workspacesID, workspaces.body, audit.VerbWorkspaceList},
		{toolsID, toolList.body, audit.VerbMCPProtocolRead},
	}
	for _, current := range cases {
		operationID := uuid.Nil
		for _, orgID := range organizations {
			row := ledger.row(t, orgID, current.requestID)
			requireReadRow(t, row, current.verb, audit.OutcomeCompleted, "", current.body)
			if operationID == uuid.Nil {
				operationID = row.Context.OperationID
			}
			if row.Context.OperationID != operationID {
				t.Fatalf("event %s operation ID = %s, want %s", row.EventID, row.Context.OperationID, operationID)
			}
			for _, target := range ledger.manifestTargets(t, row) {
				if owners[target.ID] != orgID {
					t.Fatalf("event %s in organization %s lists identity %s of organization %s",
						row.EventID, orgID, target.ID, owners[target.ID])
				}
			}
		}
	}
	for _, orgID := range organizations {
		row := ledger.row(t, orgID, workspacesID)
		if row.Context.Read.Count != 1 {
			t.Fatalf("workspace list event %s count = %d, want 1 for organization %s",
				row.EventID, row.Context.Read.Count, orgID)
		}
	}
}

// addSecondOrganization creates a second organization, adds the server actor as a member, and returns its ID.
func addSecondOrganization(t *testing.T, server *publicServer) uuid.UUID {
	t.Helper()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("datagen.ParseScale failed: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(integration.WithTestOperation(t, t.Context()), server.config, clock.Now().UnixNano(), scale)
	if err != nil {
		t.Fatalf("datagen.BootstrapIdentities failed for the second organization: %v", err)
	}
	pool, err := postgres.NewPool(t.Context(), server.config.DatabaseURL, nil)
	if err != nil {
		t.Fatalf("postgres.NewPool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	secondOrg := identities.Workspaces[0].OrgID
	member := &org.Member{ID: uuid.Nil, OrgID: secondOrg, UserID: server.userID, Role: memberRole}
	if err := postgres.NewOrgMemberRepo(pool).AddMember(t.Context(), member); err != nil {
		t.Fatalf("the test could not add user %s to organization %s: %v", server.userID, secondOrg, err)
	}
	return secondOrg
}

// identityOwners returns the organization ID for each node type, node, and property definition
// in organizations.
func identityOwners(t *testing.T, server *publicServer, organizations []uuid.UUID) map[uuid.UUID]uuid.UUID {
	t.Helper()
	stores, err := fdbadapter.NewStores(server.clusterFile, harnessTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("fdbadapter.NewStores failed: %v", err)
	}
	owners := map[uuid.UUID]uuid.UUID{}
	for _, orgID := range organizations {
		nodeTypes, err := stores.NodeTypes.List(t.Context(), orgID)
		if err != nil {
			t.Fatalf("the test could not list node types in organization %s: %v", orgID, err)
		}
		for _, nodeType := range nodeTypes {
			owners[nodeType.ID] = orgID
			views, err := stores.Views.List(t.Context(), nodeListQuery(orgID, nodeType.TypeKey))
			if err != nil {
				t.Fatalf("the test could not list %s nodes in organization %s: %v", nodeType.TypeKey, orgID, err)
			}
			for _, view := range views {
				owners[view.ID] = orgID
			}
		}
		definitions, err := stores.PropertyDefs.List(t.Context(), orgID)
		if err != nil {
			t.Fatalf("the test could not list property definitions in organization %s: %v", orgID, err)
		}
		for _, definition := range definitions {
			owners[definition.ID] = orgID
		}
	}
	return owners
}
